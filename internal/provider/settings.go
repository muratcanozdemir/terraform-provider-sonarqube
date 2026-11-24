package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/sync/errgroup"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// apiSetting is a setting value as returned by api/settings/values. The
// typed client model fixes the keys of fieldValues, which are in fact free
// maps, so the raw body is decoded here.
type apiSetting struct {
	Key         string              `json:"key"`
	Value       string              `json:"value"`
	Values      []string            `json:"values"`
	FieldValues []map[string]string `json:"fieldValues"`
	Inherited   bool                `json:"inherited"`
}

type apiSettings struct {
	Settings           []apiSetting `json:"settings"`
	SetSecuredSettings []string     `json:"setSecuredSettings"`
}

func readSettings(ctx context.Context, c *sonarqube.Client, component string, keys ...string) (*apiSettings, error) {
	_, raw, err := c.Settings.Values(ctx, &sonarqube.SettingsValuesOptions{Component: component, Keys: keys})
	if err != nil {
		return nil, err
	}
	var out apiSettings
	if err := json.Unmarshal(raw.Body, &out); err != nil {
		return nil, fmt.Errorf("decoding api/settings/values: %w", err)
	}
	return &out, nil
}

// readComponentSettings returns the component's settings. Without `keys`,
// api/settings/values omits settings that have no registered definition, so
// the configured keys are also requested explicitly, concurrently, and the
// results merged.
func readComponentSettings(ctx context.Context, c *sonarqube.Client, component string, keys []string) ([]apiSetting, error) {
	var all, named *apiSettings
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { all, err = readSettings(gctx, c, component); return })
	if len(keys) > 0 {
		g.Go(func() (err error) { named, err = readSettings(gctx, c, component, keys...); return })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := all.Settings
	if named != nil {
		seen := map[string]bool{}
		for _, s := range out {
			seen[s.Key] = true
		}
		for _, s := range named.Settings {
			if !seen[s.Key] {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func settingKeys(blocks []settingModel) []string {
	keys := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if !b.Key.IsUnknown() && !b.Key.IsNull() {
			keys = append(keys, b.Key.ValueString())
		}
	}
	return keys
}

// setSetting writes one setting. Exactly one of value, values or
// fieldValues is used, in that order of precedence.
func setSetting(ctx context.Context, c *sonarqube.Client, component, key, value string, values []string, fieldValues []map[string]string) error {
	opts := &sonarqube.SettingsSetOptions{Component: component, Key: key}
	switch {
	case value != "":
		opts.Value = value
	case len(values) > 0:
		opts.Values = values
	default:
		for _, fv := range fieldValues {
			b, err := json.Marshal(fv)
			if err != nil {
				return err
			}
			opts.FieldValues = append(opts.FieldValues, string(b))
		}
	}
	_, err := c.Settings.Set(ctx, opts)
	return err
}

func resetSettings(ctx context.Context, c *sonarqube.Client, component string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := c.Settings.Reset(ctx, &sonarqube.SettingsResetOptions{Component: component, Keys: keys})
	if isGone(err) && component != "" {
		return nil // component already deleted
	}
	return err
}

// ---------------------------------------------------------------------------
// `setting` block shared by sonarqube_project and sonarqube_portfolio

type settingModel struct {
	Key         types.String `tfsdk:"key"`
	Value       types.String `tfsdk:"value"`
	Values      types.List   `tfsdk:"values"`
	FieldValues types.List   `tfsdk:"field_values"`
}

var fieldValuesType = types.ListType{ElemType: types.MapType{ElemType: types.StringType}}

func settingBlock() schema.ListNestedBlock {
	return schema.ListNestedBlock{
		Description: "Settings of the component. When at least one block is present, the list is authoritative: settings set on the component " +
			"but not listed here are reset to their inherited value. When no block is present, component settings are not managed.",
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"key": schema.StringAttribute{Required: true, Description: "Setting key."},
				"value": schema.StringAttribute{Optional: true,
					Description: "Value of a single-value setting. Must not be empty; remove the block to reset the setting.",
					Validators:  []validator.String{stringvalidator.LengthAtLeast(1)}},
				"values": schema.ListAttribute{Optional: true, ElementType: types.StringType,
					Description: "Values of a multi-value setting. Must not be empty; remove the block to reset the setting.",
					Validators:  []validator.List{listvalidator.SizeAtLeast(1)}},
				"field_values": schema.ListAttribute{Optional: true, ElementType: types.MapType{ElemType: types.StringType},
					Description: "Field values of a property-set setting. Must not be empty; remove the block to reset the setting.",
					Validators:  []validator.List{listvalidator.SizeAtLeast(1)}},
			},
		},
	}
}

func (s settingModel) desired(ctx context.Context, diags *diag.Diagnostics) apiSetting {
	out := apiSetting{Key: s.Key.ValueString(), Value: s.Value.ValueString()}
	out.Values = stringsOf(ctx, s.Values, diags)
	if !s.FieldValues.IsNull() && !s.FieldValues.IsUnknown() {
		diags.Append(s.FieldValues.ElementsAs(ctx, &out.FieldValues, false)...)
	}
	return out
}

// validateSettingBlocks checks that each block sets exactly one value form and
// that keys are unique.
func validateSettingBlocks(blocks []settingModel, diags *diag.Diagnostics) {
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Key.IsUnknown() {
			continue
		}
		k := b.Key.ValueString()
		if isSecuredKey(b.Key) {
			diags.AddError("Secured setting", fmt.Sprintf("%q is a secured setting, whose value SonarQube never returns. Manage it with sonarqube_secured_setting.", k))
		}
		if seen[k] {
			diags.AddError("Duplicate setting", fmt.Sprintf("Setting %q is listed more than once.", k))
		}
		seen[k] = true
		n := 0
		for _, set := range []bool{!b.Value.IsNull(), !b.Values.IsNull(), !b.FieldValues.IsNull()} {
			if set {
				n++
			}
		}
		if n != 1 {
			diags.AddError("Invalid setting", fmt.Sprintf("Setting %q must set exactly one of value, values or field_values.", k))
		}
	}
}

func sameSetting(want, have apiSetting) bool {
	switch {
	case want.Value != "":
		return want.Value == have.Value || (have.Value == "" && want.Value == strings.Join(have.Values, ","))
	case len(want.Values) > 0:
		hv := have.Values
		if len(hv) == 0 && have.Value != "" {
			hv = splitStoredValues(have.Value)
		}
		return slices.Equal(want.Values, hv)
	default:
		a, _ := json.Marshal(want.FieldValues)
		b, _ := json.Marshal(have.FieldValues)
		return string(a) == string(b)
	}
}

// syncComponentSettings makes the component's own (non-inherited) settings
// equal to blocks. The component's settings are locked for the duration so
// that concurrent resources do not interleave a read-modify-write.
// extraKeys are keys from prior state, so that settings without a
// definition that were removed from the configuration are still found.
func syncComponentSettings(ctx context.Context, p *providerData, component string, blocks []settingModel, extraKeys []string, diags *diag.Diagnostics) {
	unlock := p.locks.lock("settings:" + component)
	defer unlock()

	// Ask for previously-managed keys too, so dropped ones are found and reset.
	current, err := readComponentSettings(ctx, p.client, component, append(settingKeys(blocks), extraKeys...))
	if err != nil {
		apiError(diags, "Unable to read settings of "+component, err)
		return
	}
	have := map[string]apiSetting{}
	for _, s := range current {
		if !s.Inherited {
			have[s.Key] = s
		}
	}
	want := map[string]bool{}
	for _, b := range blocks {
		d := b.desired(ctx, diags)
		want[d.Key] = true
		if h, ok := have[d.Key]; ok && sameSetting(d, h) {
			continue
		}
		if err := setSetting(ctx, p.client, component, d.Key, d.Value, d.Values, d.FieldValues); err != nil {
			apiError(diags, fmt.Sprintf("Unable to set %s on %s", d.Key, component), err)
			return
		}
	}
	var stale []string
	for k := range have {
		if !want[k] {
			stale = append(stale, k)
		}
	}
	slices.Sort(stale)
	if err := resetSettings(ctx, p.client, component, stale); err != nil {
		apiError(diags, "Unable to reset settings of "+component, err)
	}
}

// settingsState renders the component's own settings for state: configured
// settings first, in configuration order and in the form they were
// configured in, then any other setting the component has, so that drift
// shows up in the plan.
func settingsState(ctx context.Context, prior []settingModel, current []apiSetting, diags *diag.Diagnostics) []settingModel {
	byKey := map[string]apiSetting{}
	for _, s := range current {
		if !s.Inherited {
			byKey[s.Key] = s
		}
	}
	var out []settingModel
	used := map[string]bool{}
	for _, p := range prior {
		k := p.Key.ValueString()
		s, ok := byKey[k]
		if !ok || used[k] {
			continue
		}
		used[k] = true
		out = append(out, settingToModel(ctx, s, p, diags))
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		if !used[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, settingToModel(ctx, byKey[k], settingModel{}, diags))
	}
	return out
}

// settingToModel renders s in the shape of shape: a setting configured with
// `value` keeps `value` even if the server reports it as a multi-value.
func settingToModel(ctx context.Context, s apiSetting, shape settingModel, diags *diag.Diagnostics) settingModel {
	m := settingModel{
		Key:         types.StringValue(s.Key),
		Value:       types.StringNull(),
		Values:      types.ListNull(types.StringType),
		FieldValues: types.ListNull(fieldValuesType.ElemType),
	}
	switch {
	case !shape.Value.IsNull():
		v := s.Value
		if v == "" {
			v = strings.Join(s.Values, ",")
		}
		m.Value = types.StringValue(v)
	case !shape.Values.IsNull():
		vs := s.Values
		if len(vs) == 0 && s.Value != "" {
			vs = splitStoredValues(s.Value)
		}
		m.Values = stringList(vs)
	case !shape.FieldValues.IsNull():
		m.FieldValues = fieldValuesList(ctx, s.FieldValues, diags)
	case len(s.FieldValues) > 0:
		m.FieldValues = fieldValuesList(ctx, s.FieldValues, diags)
	case len(s.Values) > 0:
		m.Values = stringList(s.Values)
	default:
		m.Value = types.StringValue(s.Value)
	}
	return m
}

// splitStoredValues splits the single value SonarQube reports for a
// multi-value setting that has no definition: it joins the values with ','
// after encoding a ',' inside a value as "%2C".
func splitStoredValues(s string) []string {
	vs := strings.Split(s, ",")
	for i, v := range vs {
		vs[i] = strings.ReplaceAll(v, "%2C", ",")
	}
	return vs
}

func fieldValuesList(ctx context.Context, fvs []map[string]string, diags *diag.Diagnostics) types.List {
	elems := make([]attr.Value, 0, len(fvs))
	for _, fv := range fvs {
		m, d := types.MapValueFrom(ctx, types.StringType, fv)
		diags.Append(d...)
		elems = append(elems, m)
	}
	l, d := types.ListValue(fieldValuesType.ElemType, elems)
	diags.Append(d...)
	return l
}

// rawPost sends a v1 form POST through the client's limiter and retry policy.
// It is used where the typed options cannot express a request, such as an
// explicitly empty parameter.
func rawPost(ctx context.Context, c *sonarqube.Client, path string, form url.Values) error {
	_, err := c.Do(ctx, &sonarqube.Request{Method: "POST", Path: path, Form: form}, nil)
	return err
}
