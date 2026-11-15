package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func settingID(key, component string) string {
	if component == "" {
		return key
	}
	return key + ":" + component
}

// ---------------------------------------------------------------------------
// sonarqube_setting

var _ resource.ResourceWithValidateConfig = (*settingResource)(nil)

type settingResource struct{ resourceBase }

func newSettingResource() resource.Resource { return &settingResource{} }

type settingResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Component   types.String `tfsdk:"component"`
	Value       types.String `tfsdk:"value"`
	Values      types.List   `tfsdk:"values"`
	FieldValues types.List   `tfsdk:"field_values"`
}

func (r *settingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_setting"
}

func (r *settingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	oneOf := path.Expressions{path.MatchRoot("value"), path.MatchRoot("values"), path.MatchRoot("field_values")}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "One setting, global or on a component. Destroying it resets the setting to its inherited or default value. " +
			"Do not also manage a component's settings with `setting` blocks of `sonarqube_project` or `sonarqube_portfolio`, which are authoritative. " +
			"Import ID: `key` or `key:component`.",
		Attributes: map[string]schema.Attribute{
			"id":  schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Setting key."},
			"component": schema.StringAttribute{Optional: true, PlanModifiers: replace,
				Description: "Project, application or portfolio key. Omit for a global setting."},
			"value": schema.StringAttribute{Optional: true,
				Description: "Value of a single-value setting. Must not be empty; destroy the resource to reset the setting.",
				Validators:  []validator.String{stringvalidator.ExactlyOneOf(oneOf...), stringvalidator.LengthAtLeast(1)}},
			"values": schema.ListAttribute{Optional: true, ElementType: types.StringType,
				Description: "Values of a multi-value setting. Must not be empty; destroy the resource to reset the setting.",
				Validators:  []validator.List{listvalidator.ExactlyOneOf(oneOf...), listvalidator.SizeAtLeast(1)}},
			"field_values": schema.ListAttribute{Optional: true, ElementType: types.MapType{ElemType: types.StringType},
				Description: "Field values of a property-set setting. Must not be empty; destroy the resource to reset the setting.",
				Validators:  []validator.List{listvalidator.ExactlyOneOf(oneOf...), listvalidator.SizeAtLeast(1)}},
		},
	}
}

// ValidateConfig rejects secured keys: SonarQube never returns their value,
// so the setting would look deleted on every refresh and be set again.
func (r *settingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var key types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("key"), &key)...)
	if isSecuredKey(key) {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "Secured setting",
			fmt.Sprintf("%q is a secured setting, whose value SonarQube never returns. Manage it with sonarqube_secured_setting.", key.ValueString()))
	}
}

func isSecuredKey(key types.String) bool {
	return !key.IsNull() && !key.IsUnknown() && strings.HasSuffix(key.ValueString(), ".secured")
}

func (m *settingResourceModel) block() settingModel {
	return settingModel{Key: m.Key, Value: m.Value, Values: m.Values, FieldValues: m.FieldValues}
}

func (r *settingResource) write(ctx context.Context, m *settingResourceModel) error {
	var diags diag.Diagnostics
	d := m.block().desired(ctx, &diags)
	if diags.HasError() {
		return fmt.Errorf("invalid setting value: %v", diags)
	}
	unlock := r.p.locks.lock("settings:" + m.Component.ValueString())
	defer unlock()
	return setSetting(ctx, r.c(), m.Component.ValueString(), d.Key, d.Value, d.Values, d.FieldValues)
}

func (r *settingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan settingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set "+plan.Key.ValueString(), err)
		return
	}
	plan.ID = types.StringValue(settingID(plan.Key.ValueString(), plan.Component.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *settingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state settingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key, component := state.Key.ValueString(), state.Component.ValueString()
	settings, err := readSettings(ctx, r.c(), component, key)
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read "+key, err)
		return
	}
	for _, s := range settings.Settings {
		// "inherited" means the setting is not set at this level any more.
		if s.Key != key || s.Inherited {
			continue
		}
		shape := state.block()
		if shape.Value.IsNull() && shape.Values.IsNull() && shape.FieldValues.IsNull() {
			shape = settingModel{} // import: take the server's form
		}
		m := settingToModel(ctx, s, shape, &resp.Diagnostics)
		state.Value, state.Values, state.FieldValues = m.Value, m.Values, m.FieldValues
		state.ID = types.StringValue(settingID(key, component))
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *settingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan settingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set "+plan.Key.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *settingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state settingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := r.p.locks.lock("settings:" + state.Component.ValueString())
	defer unlock()
	if err := resetSettings(ctx, r.c(), state.Component.ValueString(), []string{state.Key.ValueString()}); err != nil {
		apiError(&resp.Diagnostics, "Unable to reset "+state.Key.ValueString(), err)
	}
}

func (r *settingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, component, _ := strings.Cut(req.ID, ":")
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	if component != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("component"), component)...)
	}
}

// ---------------------------------------------------------------------------
// sonarqube_secured_setting

var _ resource.ResourceWithValidateConfig = (*securedSettingResource)(nil)

type securedSettingResource struct{ resourceBase }

func newSecuredSettingResource() resource.Resource { return &securedSettingResource{} }

type securedSettingModel struct {
	ID             types.String `tfsdk:"id"`
	Key            types.String `tfsdk:"key"`
	Component      types.String `tfsdk:"component"`
	Value          types.String `tfsdk:"value"`
	ValueWO        types.String `tfsdk:"value_wo"`
	ValueWOVersion types.String `tfsdk:"value_wo_version"`
}

func (r *securedSettingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secured_setting"
}

func (r *securedSettingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A secured setting (key ending in `.secured`). SonarQube never returns its value, so only its presence is " +
			"checked for drift. Import ID: `key` or `key:component`.",
		Attributes: map[string]schema.Attribute{
			"id":  schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Setting key."},
			"component": schema.StringAttribute{Optional: true, PlanModifiers: replace,
				Description: "Project, application or portfolio key. Omit for a global setting."},
			"value": schema.StringAttribute{Optional: true, Sensitive: true,
				Description: "Value. Stored in state; prefer `value_wo`.",
				Validators:  []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("value"), path.MatchRoot("value_wo"))}},
			"value_wo": schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Write-only value, never stored in state (Terraform 1.11+). Change `value_wo_version` to apply a new one.",
				Validators:  []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("value_wo_version"))}},
			"value_wo_version": schema.StringAttribute{Optional: true, Description: "Any value; changing it applies the current `value_wo`."},
		},
	}
}

func (r *securedSettingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var key types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("key"), &key)...)
	if !key.IsUnknown() && !key.IsNull() && !strings.HasSuffix(key.ValueString(), ".secured") {
		resp.Diagnostics.AddAttributeWarning(path.Root("key"), "Not a secured setting",
			fmt.Sprintf("%q does not end in .secured; SonarQube will return its value in clear text. Use sonarqube_setting for it.", key.ValueString()))
	}
}

func (r *securedSettingResource) write(ctx context.Context, plan, cfg *securedSettingModel) error {
	v := plan.Value.ValueString()
	if !cfg.ValueWO.IsNull() {
		v = cfg.ValueWO.ValueString()
	}
	unlock := r.p.locks.lock("settings:" + plan.Component.ValueString())
	defer unlock()
	return setSetting(ctx, r.c(), plan.Component.ValueString(), plan.Key.ValueString(), v, nil, nil)
}

func (r *securedSettingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg securedSettingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.write(ctx, &plan, &cfg); err != nil {
		apiError(&resp.Diagnostics, "Unable to set "+plan.Key.ValueString(), err)
		return
	}
	plan.ID = types.StringValue(settingID(plan.Key.ValueString(), plan.Component.ValueString()))
	plan.ValueWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securedSettingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state securedSettingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.Key.ValueString()
	settings, err := readSettings(ctx, r.c(), state.Component.ValueString(), key)
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read "+key, err)
		return
	}
	for _, k := range settings.SetSecuredSettings {
		if k == key {
			state.ID = types.StringValue(settingID(key, state.Component.ValueString()))
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *securedSettingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg securedSettingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := !plan.Value.Equal(state.Value) || (!cfg.ValueWO.IsNull() && !plan.ValueWOVersion.Equal(state.ValueWOVersion))
	if changed {
		if err := r.write(ctx, &plan, &cfg); err != nil {
			apiError(&resp.Diagnostics, "Unable to set "+plan.Key.ValueString(), err)
			return
		}
	}
	plan.ValueWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securedSettingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state securedSettingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := r.p.locks.lock("settings:" + state.Component.ValueString())
	defer unlock()
	if err := resetSettings(ctx, r.c(), state.Component.ValueString(), []string{state.Key.ValueString()}); err != nil {
		apiError(&resp.Diagnostics, "Unable to reset "+state.Key.ValueString(), err)
	}
}

func (r *securedSettingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, component, _ := strings.Cut(req.ID, ":")
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	if component != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("component"), component)...)
	}
}

func (r *settingResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *securedSettingResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
