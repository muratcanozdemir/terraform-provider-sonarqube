package provider

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// Quality gates

type gateDataModel struct {
	ID        types.String     `tfsdk:"id"`
	Name      types.String     `tfsdk:"name"`
	CopyFrom  types.String     `tfsdk:"copy_from"`
	IsDefault types.Bool       `tfsdk:"is_default"`
	IsBuiltIn types.Bool       `tfsdk:"is_built_in"`
	Condition []conditionModel `tfsdk:"condition"`
}

func gateAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":          schema.StringAttribute{Computed: true, Description: "The gate name."},
		"name":        schema.StringAttribute{Computed: true, Description: "Gate name."},
		"copy_from":   schema.StringAttribute{Computed: true, Description: "Always null: SonarQube does not record what a gate was copied from. Kept for compatibility."},
		"is_default":  schema.BoolAttribute{Computed: true, Description: "Whether this is the default gate for new projects."},
		"is_built_in": schema.BoolAttribute{Computed: true, Description: "Whether this gate ships with SonarQube and cannot be changed."},
		"condition": schema.ListNestedAttribute{Computed: true, Description: "Conditions, sorted by metric.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":        schema.StringAttribute{Computed: true, Description: "Condition ID."},
				"metric":    schema.StringAttribute{Computed: true, Description: "Metric key the condition checks."},
				"op":        schema.StringAttribute{Computed: true, Description: "`LT` or `GT`."},
				"threshold": schema.StringAttribute{Computed: true, Description: "Value that fails the condition when crossed in the direction of `op`."},
			}}},
	}
}

// readGate returns nil when the gate does not exist.
func readGate(ctx context.Context, c *sonarqube.Client, name string) (*gateDataModel, error) {
	g, _, err := c.Qualitygates.Show(ctx, &sonarqube.QualitygatesShowOptions{Name: name})
	if isGone(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := &gateDataModel{ID: types.StringValue(g.Name), Name: types.StringValue(g.Name), CopyFrom: types.StringNull(),
		IsDefault: types.BoolValue(g.IsDefault), IsBuiltIn: types.BoolValue(g.IsBuiltIn), Condition: []conditionModel{}}
	conds := slices.Clone(g.Conditions)
	slices.SortFunc(conds, func(a, b *sonarqube.QualitygatesShowResponseCondition) int { return cmp.Compare(a.Metric, b.Metric) })
	for _, cd := range conds {
		m.Condition = append(m.Condition, conditionFrom(cd))
	}
	return m, nil
}

type qualityGateDataSource struct{ dataSourceBase }

func newQualityGateDataSource() datasource.DataSource { return &qualityGateDataSource{} }

func (d *qualityGateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualitygate"
}

func (d *qualityGateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := gateAttributes()
	attrs["name"] = schema.StringAttribute{Required: true, Description: "Exact gate name."}
	resp.Schema = schema.Schema{Description: "Looks up a quality gate and its conditions.", Attributes: attrs}
}

func (d *qualityGateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := readGate(ctx, d.c(), name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality gate "+name.ValueString(), err)
		return
	}
	if m == nil {
		resp.Diagnostics.AddError("Quality gate not found", fmt.Sprintf("No quality gate is named %q.", name.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

type qualityGatesDataSource struct{ dataSourceBase }

func newQualityGatesDataSource() datasource.DataSource { return &qualityGatesDataSource{} }

type qualityGatesDataModel struct {
	ID            types.String    `tfsdk:"id"`
	Name          types.String    `tfsdk:"name"`
	IgnoreMissing types.Bool      `tfsdk:"ignore_missing"`
	QualityGates  []gateDataModel `tfsdk:"quality_gates"`
}

func (d *qualityGatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualitygates"
}

func (d *qualityGatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists quality gates with their conditions, sorted by name.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"name":           schema.StringAttribute{Optional: true, Description: "Only return the gate with this exact name. Omit to list every gate."},
			"ignore_missing": schema.BoolAttribute{Optional: true, Description: "With `name`, return an empty list rather than fail when the gate does not exist."},
			"quality_gates": schema.ListNestedAttribute{Computed: true, Description: "Matching gates.",
				NestedObject: schema.NestedAttributeObject{Attributes: gateAttributes()}},
		},
	}
}

func (d *qualityGatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m qualityGatesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = types.StringValue("qualitygates:" + m.Name.ValueString())
	m.QualityGates = []gateDataModel{}
	var names []string
	if !m.Name.IsNull() {
		names = []string{m.Name.ValueString()}
	} else {
		l, _, err := d.c().Qualitygates.List(ctx)
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to list quality gates", err)
			return
		}
		for _, g := range l.Qualitygates {
			names = append(names, g.Name)
		}
	}
	var mu sync.Mutex
	err := forEach(ctx, names, func(ctx context.Context, name string) error {
		g, err := readGate(ctx, d.c(), name)
		if err != nil {
			return fmt.Errorf("reading quality gate %s: %w", name, err)
		}
		if g != nil {
			mu.Lock()
			m.QualityGates = append(m.QualityGates, *g)
			mu.Unlock()
		}
		return nil
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality gates", err)
		return
	}
	if !m.Name.IsNull() && len(m.QualityGates) == 0 && !m.IgnoreMissing.ValueBool() {
		resp.Diagnostics.AddError("Quality gate not found", fmt.Sprintf("No quality gate is named %q. Set ignore_missing = true to return an empty list instead.", m.Name.ValueString()))
		return
	}
	slices.SortFunc(m.QualityGates, func(a, b gateDataModel) int { return cmp.Compare(a.Name.ValueString(), b.Name.ValueString()) })
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// Quality profiles

type profileDataModel struct {
	ID              types.String `tfsdk:"id"`
	Key             types.String `tfsdk:"key"`
	Name            types.String `tfsdk:"name"`
	Language        types.String `tfsdk:"language"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	IsBuiltIn       types.Bool   `tfsdk:"is_built_in"`
	Parent          types.String `tfsdk:"parent"`
	ActiveRuleCount types.Int64  `tfsdk:"active_rule_count"`
}

func profileData(p *sonarqube.QualityprofilesSearchResponseProfile) profileDataModel {
	return profileDataModel{
		ID: types.StringValue(p.Key), Key: types.StringValue(p.Key), Name: types.StringValue(p.Name),
		Language: types.StringValue(p.Language), IsDefault: types.BoolValue(p.IsDefault), IsBuiltIn: types.BoolValue(p.IsBuiltIn),
		Parent: strOrNull(p.ParentName), ActiveRuleCount: types.Int64Value(p.ActiveRuleCount),
	}
}

func profileAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, Description: "Profile key."},
		"key":               schema.StringAttribute{Computed: true, Description: "Profile key."},
		"name":              schema.StringAttribute{Computed: true, Description: "Profile name."},
		"language":          schema.StringAttribute{Computed: true, Description: "Language key."},
		"is_default":        schema.BoolAttribute{Computed: true, Description: "Whether this is the default profile for its language."},
		"is_built_in":       schema.BoolAttribute{Computed: true, Description: "Whether this profile ships with SonarQube and cannot be changed."},
		"parent":            schema.StringAttribute{Computed: true, Description: "Name of the parent profile, or null."},
		"active_rule_count": schema.Int64Attribute{Computed: true, Description: "Number of rules active in the profile, inherited ones included."},
	}
}

type qualityProfileDataSource struct{ dataSourceBase }

func newQualityProfileDataSource() datasource.DataSource { return &qualityProfileDataSource{} }

var _ datasource.DataSourceWithValidateConfig = (*qualityProfileDataSource)(nil)

func (d *qualityProfileDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofile"
}

func (d *qualityProfileDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := profileAttributes()
	attrs["key"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Profile key. Set either this, or `name` and `language`."}
	attrs["name"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Exact profile name; requires `language`."}
	attrs["language"] = schema.StringAttribute{Optional: true, Computed: true, Description: "Language key, e.g. `java`; requires `name`."}
	resp.Schema = schema.Schema{Description: "Looks up a quality profile by key, or by name and language.", Attributes: attrs}
}

func (d *qualityProfileDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var m profileDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Key.IsUnknown() || m.Name.IsUnknown() || m.Language.IsUnknown() {
		return
	}
	byKey, byName := !m.Key.IsNull(), !m.Name.IsNull() || !m.Language.IsNull()
	switch {
	case byKey && byName:
		resp.Diagnostics.AddError("Conflicting lookup", "Set either key, or name and language, not both.")
	case !byKey && (m.Name.IsNull() || m.Language.IsNull()):
		resp.Diagnostics.AddError("Incomplete lookup", "Set key, or both name and language: profile names are only unique per language.")
	}
}

func (d *qualityProfileDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m profileDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := findProfile(ctx, d.c(), m.Key.ValueString(), m.Language.ValueString(), m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality profile", err)
		return
	}
	if p == nil {
		what := fmt.Sprintf("key %q", m.Key.ValueString())
		if m.Key.IsNull() {
			what = fmt.Sprintf("name %q for language %q", m.Name.ValueString(), m.Language.ValueString())
		}
		resp.Diagnostics.AddError("Quality profile not found", "No quality profile has the "+what+".")
		return
	}
	m = profileData(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type qualityProfilesDataSource struct{ dataSourceBase }

func newQualityProfilesDataSource() datasource.DataSource { return &qualityProfilesDataSource{} }

type qualityProfilesDataModel struct {
	ID              types.String       `tfsdk:"id"`
	Name            types.String       `tfsdk:"name"`
	Language        types.String       `tfsdk:"language"`
	QualityProfiles []profileDataModel `tfsdk:"quality_profiles"`
}

func (d *qualityProfilesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofiles"
}

func (d *qualityProfilesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists quality profiles, sorted by language and name.",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"name":     schema.StringAttribute{Optional: true, Description: "Only return profiles with this exact name."},
			"language": schema.StringAttribute{Optional: true, Description: "Only return profiles of this language."},
			"quality_profiles": schema.ListNestedAttribute{Computed: true, Description: "Matching profiles.",
				NestedObject: schema.NestedAttributeObject{Attributes: profileAttributes()}},
		},
	}
}

func (d *qualityProfilesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m qualityProfilesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := d.c().Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: m.Language.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list quality profiles", err)
		return
	}
	profiles := slices.Clone(res.Profiles)
	slices.SortFunc(profiles, func(a, b *sonarqube.QualityprofilesSearchResponseProfile) int {
		return cmp.Or(cmp.Compare(a.Language, b.Language), cmp.Compare(a.Name, b.Name))
	})
	m.QualityProfiles = []profileDataModel{}
	for _, p := range profiles {
		if m.Name.IsNull() || p.Name == m.Name.ValueString() {
			m.QualityProfiles = append(m.QualityProfiles, profileData(p))
		}
	}
	m.ID = types.StringValue("qualityprofiles:" + m.Language.ValueString() + ":" + m.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// Rules

// searchLimit is Elasticsearch's result window: api/rules/search cannot page
// past it.
const searchLimit = 10000

func searchRules(ctx context.Context, c *sonarqube.Client, opts sonarqube.RulesSearchOptions) ([]*sonarqube.RulesSearchResponseRule, error) {
	opts.F = []string{"name", "severity", "status", "lang", "repo", "templateKey", "isTemplate"}
	var reported int64
	rules, err := collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.RulesSearchResponseRule, int64, error) {
		o := opts
		o.P, o.Ps = &page, &size
		res, _, err := c.Rules.Search(ctx, &o)
		if err != nil {
			return nil, 0, err
		}
		reported = total(res.Paging)
		if reported > searchLimit {
			return nil, 0, fmt.Errorf("the search matches %d rules; SonarQube returns at most %d, so add filters to narrow it", reported, searchLimit)
		}
		return res.Rules, reported, nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rules, func(a, b *sonarqube.RulesSearchResponseRule) int { return cmp.Compare(a.Key, b.Key) })
	return rules, nil
}

type ruleSummaryModel struct {
	Key      types.String `tfsdk:"key"`
	Name     types.String `tfsdk:"name"`
	Severity types.String `tfsdk:"severity"`
	Type     types.String `tfsdk:"type"`
	Status   types.String `tfsdk:"status"`
	Language types.String `tfsdk:"language"`
	Repo     types.String `tfsdk:"repo"`
}

func ruleSummaries(rules []*sonarqube.RulesSearchResponseRule) []ruleSummaryModel {
	out := make([]ruleSummaryModel, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleSummaryModel{
			Key: types.StringValue(r.Key), Name: types.StringValue(r.Name), Severity: strOrNull(r.Severity),
			Type: strOrNull(r.Type), Status: types.StringValue(r.Status), Language: types.StringValue(r.Lang), Repo: types.StringValue(r.Repo),
		})
	}
	return out
}

func rulesAttribute(desc string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Computed: true, Description: desc + " Sorted by key.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"key":      schema.StringAttribute{Computed: true, Description: "Rule key, e.g. `java:S1144`."},
			"name":     schema.StringAttribute{Computed: true, Description: "Rule name."},
			"severity": schema.StringAttribute{Computed: true, Description: "Default severity (`INFO`, `MINOR`, `MAJOR`, `CRITICAL` or `BLOCKER`)."},
			"type":     schema.StringAttribute{Computed: true, Description: "Rule type (`CODE_SMELL`, `BUG`, `VULNERABILITY` or `SECURITY_HOTSPOT`)."},
			"status":   schema.StringAttribute{Computed: true, Description: "Rule status (`READY`, `BETA`, `DEPRECATED` or `REMOVED`)."},
			"language": schema.StringAttribute{Computed: true, Description: "Language key."},
			"repo":     schema.StringAttribute{Computed: true, Description: "Rule repository."},
		}}}
}

// profileRulesDataSource lists the rules activated, or not activated, in a
// quality profile.
type profileRulesDataSource struct {
	dataSourceBase
	active bool
}

func newQualityProfileActiveRulesDataSource() datasource.DataSource {
	return &profileRulesDataSource{active: true}
}

func newQualityProfileDeactivatedRulesDataSource() datasource.DataSource {
	return &profileRulesDataSource{active: false}
}

type profileRulesDataModel struct {
	ID    types.String       `tfsdk:"id"`
	Key   types.String       `tfsdk:"key"`
	Rules []ruleSummaryModel `tfsdk:"rules"`
}

func (d *profileRulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	if d.active {
		resp.TypeName = req.ProviderTypeName + "_qualityprofile_active_rules"
	} else {
		resp.TypeName = req.ProviderTypeName + "_qualityprofile_deactivated_rules"
	}
}

func (d *profileRulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	desc, list := "Lists the rules active in a quality profile.", "Active rules."
	if !d.active {
		desc, list = "Lists the rules of a profile's language that are not active in the profile.", "Inactive rules."
	}
	resp.Schema = schema.Schema{
		Description: desc,
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"key":   schema.StringAttribute{Required: true, Description: "Quality profile key, e.g. from `sonarqube_qualityprofile`."},
			"rules": rulesAttribute(list),
		},
	}
}

func (d *profileRulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m profileRulesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.Key.ValueString()
	// rules/search silently ignores an unknown profile and returns every
	// rule, so confirm the profile exists first.
	p, err := findProfile(ctx, d.c(), key, "", "")
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality profile "+key, err)
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("Quality profile not found", fmt.Sprintf("No quality profile has the key %q.", key))
		return
	}
	rules, err := searchRules(ctx, d.c(), sonarqube.RulesSearchOptions{Qprofile: key, Activation: new(d.active)})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list rules of quality profile "+key, err)
		return
	}
	m.ID, m.Rules = types.StringValue(key), ruleSummaries(rules)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type rulesDataSource struct{ dataSourceBase }

func newRulesDataSource() datasource.DataSource { return &rulesDataSource{} }

type rulesDataModel struct {
	ID           types.String       `tfsdk:"id"`
	Search       types.String       `tfsdk:"search"`
	Languages    types.Set          `tfsdk:"languages"`
	Repositories types.Set          `tfsdk:"repositories"`
	Severities   types.Set          `tfsdk:"severities"`
	Statuses     types.Set          `tfsdk:"statuses"`
	Tags         types.Set          `tfsdk:"tags"`
	Types        types.Set          `tfsdk:"types"`
	TemplateKey  types.String       `tfsdk:"template_key"`
	IsTemplate   types.Bool         `tfsdk:"is_template"`
	Rules        []ruleSummaryModel `tfsdk:"rules"`
}

func (d *rulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rules"
}

func (d *rulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filter := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists rules matching a set of filters, e.g. to activate them in a profile with `for_each`. Filters combine with AND; " +
			"values within one filter with OR. SonarQube returns at most 10000 rules for one search; a broader search fails rather than return a partial list.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"search":       schema.StringAttribute{Optional: true, Description: "Free-text search, as in the rules page search box."},
			"languages":    filter("Language keys, e.g. `java`."),
			"repositories": filter("Rule repository keys, e.g. `java`, `csharpsquid`."),
			"severities":   filter("`INFO`, `MINOR`, `MAJOR`, `CRITICAL` or `BLOCKER`."),
			"statuses":     filter("`READY`, `BETA`, `DEPRECATED` or `REMOVED`."),
			"tags":         filter("Rule tags."),
			"types":        filter("`CODE_SMELL`, `BUG`, `VULNERABILITY` or `SECURITY_HOTSPOT`."),
			"template_key": schema.StringAttribute{Optional: true, Description: "Only custom rules created from this template rule."},
			"is_template":  schema.BoolAttribute{Optional: true, Description: "Only template rules (true) or only non-template rules (false)."},
			"rules":        rulesAttribute("Matching rules."),
		},
	}
}

func (d *rulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m rulesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sorted := func(s types.Set) []string {
		v := stringsOf(ctx, s, &resp.Diagnostics)
		slices.Sort(v)
		return v
	}
	opts := sonarqube.RulesSearchOptions{
		Q: m.Search.ValueString(), Languages: sorted(m.Languages), Repositories: sorted(m.Repositories),
		Severities: sorted(m.Severities), Statuses: sorted(m.Statuses), Tags: sorted(m.Tags), Types: sorted(m.Types),
		TemplateKey: m.TemplateKey.ValueString(),
	}
	if !m.IsTemplate.IsNull() {
		opts.IsTemplate = new(m.IsTemplate.ValueBool())
	}
	if resp.Diagnostics.HasError() {
		return
	}
	rules, err := searchRules(ctx, d.c(), opts)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to search rules", err)
		return
	}
	m.Rules = ruleSummaries(rules)
	m.ID = types.StringValue(strings.Join([]string{opts.Q, strings.Join(opts.Languages, ","), strings.Join(opts.Repositories, ","),
		strings.Join(opts.Severities, ","), strings.Join(opts.Statuses, ","), strings.Join(opts.Tags, ","), strings.Join(opts.Types, ","),
		opts.TemplateKey}, "|"))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type ruleDataSource struct{ dataSourceBase }

func newRuleDataSource() datasource.DataSource { return &ruleDataSource{} }

type ruleDataModel struct {
	ID                  types.String `tfsdk:"id"`
	Key                 types.String `tfsdk:"key"`
	Name                types.String `tfsdk:"name"`
	MarkdownDescription types.String `tfsdk:"markdown_description"`
	Severity            types.String `tfsdk:"severity"`
	Status              types.String `tfsdk:"status"`
	TemplateKey         types.String `tfsdk:"template_key"`
	IsTemplate          types.Bool   `tfsdk:"is_template"`
	Type                types.String `tfsdk:"type"`
	Language            types.String `tfsdk:"language"`
	Repo                types.String `tfsdk:"repo"`
	Params              types.Map    `tfsdk:"params"`
}

func (d *ruleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rule"
}

func (d *ruleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a rule by key.",
		Attributes: map[string]schema.Attribute{
			"id":                   schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"key":                  schema.StringAttribute{Required: true, Description: "Rule key, e.g. `java:S1144`."},
			"name":                 schema.StringAttribute{Computed: true, Description: "Rule name."},
			"markdown_description": schema.StringAttribute{Computed: true, Description: "Description of a custom rule; null for built-in rules, which use description sections."},
			"severity":             schema.StringAttribute{Computed: true, Description: "Default severity (`INFO`, `MINOR`, `MAJOR`, `CRITICAL` or `BLOCKER`)."},
			"status":               schema.StringAttribute{Computed: true, Description: "Rule status (`READY`, `BETA`, `DEPRECATED` or `REMOVED`)."},
			"template_key":         schema.StringAttribute{Computed: true, Description: "Template of a custom rule, or null."},
			"is_template":          schema.BoolAttribute{Computed: true, Description: "Whether the rule is a template that custom rules (`sonarqube_rule`) are created from."},
			"type":                 schema.StringAttribute{Computed: true, Description: "Rule type (`CODE_SMELL`, `BUG`, `VULNERABILITY` or `SECURITY_HOTSPOT`)."},
			"language":             schema.StringAttribute{Computed: true, Description: "Language key."},
			"repo":                 schema.StringAttribute{Computed: true, Description: "Rule repository key."},
			"params":               schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Parameter defaults by name."},
		},
	}
}

func (d *ruleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m ruleDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.Key.ValueString()
	// The typed model omits mdDesc and templateKey.
	var res struct {
		Rule struct {
			Key         string `json:"key"`
			Name        string `json:"name"`
			MdDesc      string `json:"mdDesc"`
			Severity    string `json:"severity"`
			Status      string `json:"status"`
			TemplateKey string `json:"templateKey"`
			IsTemplate  bool   `json:"isTemplate"`
			Type        string `json:"type"`
			Lang        string `json:"lang"`
			Repo        string `json:"repo"`
			Params      []struct {
				Key          string `json:"key"`
				DefaultValue string `json:"defaultValue"`
			} `json:"params"`
		} `json:"rule"`
	}
	_, err := d.c().Do(ctx, &sonarqube.Request{Method: "GET", Path: "/api/rules/show", Query: map[string][]string{"key": {key}}}, &res)
	if isGone(err) {
		resp.Diagnostics.AddError("Rule not found", fmt.Sprintf("No rule has the key %q. Keys have the form <repository>:<key>, e.g. java:S1144.", key))
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+key, err)
		return
	}
	r := res.Rule
	params := map[string]attr.Value{}
	for _, p := range r.Params {
		params[p.Key] = types.StringValue(p.DefaultValue)
	}
	m = ruleDataModel{
		ID: types.StringValue(r.Key), Key: types.StringValue(r.Key), Name: types.StringValue(r.Name),
		MarkdownDescription: strOrNull(r.MdDesc), Severity: strOrNull(r.Severity), Status: types.StringValue(r.Status),
		TemplateKey: strOrNull(r.TemplateKey), IsTemplate: types.BoolValue(r.IsTemplate), Type: strOrNull(r.Type),
		Language: types.StringValue(r.Lang), Repo: types.StringValue(r.Repo), Params: types.MapValueMust(types.StringType, params),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_languages

type languagesDataSource struct{ dataSourceBase }

func newLanguagesDataSource() datasource.DataSource { return &languagesDataSource{} }

type languageModel struct {
	Key  types.String `tfsdk:"key"`
	Name types.String `tfsdk:"name"`
}

type languagesDataModel struct {
	ID        types.String    `tfsdk:"id"`
	Search    types.String    `tfsdk:"search"`
	Languages []languageModel `tfsdk:"languages"`
}

func (d *languagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_languages"
}

func (d *languagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the languages the server's analyzers support, sorted by key.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"search": schema.StringAttribute{Optional: true, Description: "Pattern matched against language keys and names."},
			"languages": schema.ListNestedAttribute{Computed: true, Description: "Matching languages.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"key":  schema.StringAttribute{Computed: true, Description: "Language key."},
					"name": schema.StringAttribute{Computed: true, Description: "Language name."},
				}}},
		},
	}
}

func (d *languagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m languagesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := d.c().Languages.List(ctx, &sonarqube.LanguagesListOptions{Q: m.Search.ValueString(), Ps: new(0)})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list languages", err)
		return
	}
	langs := slices.Clone(res.Languages)
	slices.SortFunc(langs, func(a, b *sonarqube.LanguagesListResponseLanguage) int { return cmp.Compare(a.Key, b.Key) })
	m.Languages = make([]languageModel, 0, len(langs))
	for _, l := range langs {
		m.Languages = append(m.Languages, languageModel{Key: types.StringValue(l.Key), Name: types.StringValue(l.Name)})
	}
	m.ID = types.StringValue("languages:" + m.Search.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
