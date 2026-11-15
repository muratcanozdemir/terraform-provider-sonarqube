package provider

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_project

type projectDataSource struct{ dataSourceBase }

func newProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDataModel struct {
	ID                 types.String `tfsdk:"id"`
	Project            types.String `tfsdk:"project"`
	Name               types.String `tfsdk:"name"`
	Visibility         types.String `tfsdk:"visibility"`
	Tags               types.Set    `tfsdk:"tags"`
	RetrieveBadgeToken types.Bool   `tfsdk:"retrieve_badge_token"`
	BadgeToken         types.String `tfsdk:"badge_token"`
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a project by key.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, Description: "The project key."},
			"project":    schema.StringAttribute{Required: true, Description: "Project key."},
			"name":       schema.StringAttribute{Computed: true, Description: "Project name."},
			"visibility": schema.StringAttribute{Computed: true, Description: "`public` or `private`."},
			"tags":       schema.SetAttribute{Computed: true, Description: "Project tags.", ElementType: types.StringType},
			"retrieve_badge_token": schema.BoolAttribute{Optional: true,
				Description: "Read the badge token (default true). Set false to skip the call, e.g. without 'Browse' permission on the project."},
			"badge_token": schema.StringAttribute{Computed: true, Description: "Badge token, or null when not retrieved."},
		},
	}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r := &projectResource{resourceBase{d.p}}
	pm := projectModel{ID: m.Project, RetrieveBadgeToken: m.RetrieveBadgeToken}
	if !r.readInto(ctx, &pm, &resp.Diagnostics, false) {
		resp.Diagnostics.AddError("Project not found", fmt.Sprintf("No project has the key %q, or the provider's credentials cannot browse it.", m.Project.ValueString()))
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID, m.Name, m.Visibility, m.Tags, m.BadgeToken = pm.ID, pm.Name, pm.Visibility, pm.Tags, pm.BadgeToken
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_portfolio

type portfolioDataSource struct{ dataSourceBase }

func newPortfolioDataSource() datasource.DataSource { return &portfolioDataSource{} }

func (d *portfolioDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_portfolio"
}

func (d *portfolioDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a portfolio by key. Requires Enterprise edition or above.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"key":            schema.StringAttribute{Required: true, Description: "Portfolio key."},
			"name":           schema.StringAttribute{Computed: true, Description: "Portfolio name."},
			"description":    schema.StringAttribute{Computed: true, Description: "Portfolio description."},
			"qualifier":      schema.StringAttribute{Computed: true, Description: "`VW` for a portfolio, `SVW` for a sub-portfolio."},
			"visibility":     schema.StringAttribute{Computed: true, Description: "Portfolio visibility, `public` or `private`."},
			"selection_mode": schema.StringAttribute{Computed: true, Description: "`NONE`, `MANUAL`, `TAGS`, `REGEXP` or `REST`."},
			"branch":         schema.StringAttribute{Computed: true, Description: "Branch analysed for TAGS, REGEXP and REST selection."},
			"tags":           schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Project tags, for TAGS selection."},
			"regexp":         schema.StringAttribute{Computed: true, Description: "Project key pattern, for REGEXP selection."},
			"selected_projects": schema.SetNestedAttribute{Computed: true, Description: "Projects, for MANUAL selection.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"project_key":       schema.StringAttribute{Computed: true, Description: "Key of a project selected manually into the portfolio."},
					"selected_branches": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Selected branches; null means the main branch."},
				}}},
			"applications": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Keys of applications in the portfolio."},
			"portfolios":   schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Keys of the portfolios included as sub-portfolios."},
			"application_branches": schema.MapAttribute{Computed: true, ElementType: types.SetType{ElemType: types.StringType},
				Description: "Branches selected for each application of the portfolio, by application key."},
		},
	}
}

func (d *portfolioDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var key types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("key"), &key)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r := &portfolioResource{resourceBase{d.p}}
	// Non-null priors make readInto report empty collections as [] not null.
	m := portfolioModel{ID: key, Description: types.StringValue(""), Branch: types.StringValue(""), Regexp: types.StringValue(""),
		Tags: types.ListValueMust(types.StringType, nil), SelectedProjects: types.SetValueMust(selectedProjectType, nil),
		Applications: types.SetValueMust(types.StringType, nil), Portfolios: types.SetValueMust(types.StringType, nil),
		ApplicationBranches: types.MapNull(applicationBranchesType.ElemType)}
	found, err := r.readInto(ctx, &m, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key.ValueString(), err)
		return
	}
	if !found {
		resp.Diagnostics.AddError("Portfolio not found", fmt.Sprintf("No portfolio has the key %q.", key.ValueString()))
		return
	}
	// readInto reports branches only for applications already in the
	// model; a lookup reports them all.
	p, err := r.show(ctx, key.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key.ValueString(), err)
		return
	}
	branches := map[string]attr.Value{}
	for app, b := range p.appBranches() {
		branches[app] = stringSet(b)
	}
	v, diags := types.MapValue(applicationBranchesType.ElemType, branches)
	resp.Diagnostics.Append(diags...)
	m.ApplicationBranches = v
	m.ID = m.Key
	m.Description, m.Branch, m.Regexp = emptyToNull(m.Description), emptyToNull(m.Branch), emptyToNull(m.Regexp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func emptyToNull(s types.String) types.String {
	if s.ValueString() == "" {
		return types.StringNull()
	}
	return s
}

// ---------------------------------------------------------------------------
// sonarqube_measures

type measuresDataSource struct{ dataSourceBase }

func newMeasuresDataSource() datasource.DataSource { return &measuresDataSource{} }

type measureModel struct {
	Metric types.String `tfsdk:"metric"`
	Value  types.String `tfsdk:"value"`
}

type measuresDataModel struct {
	ID          types.String   `tfsdk:"id"`
	Component   types.String   `tfsdk:"component"`
	MetricKeys  types.List     `tfsdk:"metric_keys"`
	Branch      types.String   `tfsdk:"branch"`
	PullRequest types.String   `tfsdk:"pull_request"`
	Measures    []measureModel `tfsdk:"measures"`
	Values      types.Map      `tfsdk:"values"`
}

func (d *measuresDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_measures"
}

func (d *measuresDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads metric measures of a project, branch or pull request.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"component":   schema.StringAttribute{Required: true, Description: "Component (project) key."},
			"metric_keys": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Metric keys, e.g. `coverage`, `ncloc`, `new_violations`."},
			"branch":      schema.StringAttribute{Optional: true, Description: "Branch name; defaults to the main branch. Developer edition or above."},
			"pull_request": schema.StringAttribute{Optional: true,
				Description: "Pull request ID. Developer edition or above. Conflicts with `branch`."},
			"measures": schema.ListNestedAttribute{Computed: true, Description: "Measures in `metric_keys` order. Metrics with no value are omitted.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"metric": schema.StringAttribute{Computed: true, Description: "Metric key."},
					"value":  schema.StringAttribute{Computed: true, Description: "The value; for new-code (`new_*`) metrics, the new-code period value."},
				}}},
			"values": schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "The same measures keyed by metric, e.g. `values[\"coverage\"]`."},
		},
	}
}

func (d *measuresDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m measuresDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Branch.IsNull() && !m.PullRequest.IsNull() {
		resp.Diagnostics.AddError("Conflicting attributes", "Set branch or pull_request, not both.")
		return
	}
	keys := stringsOf(ctx, m.MetricKeys, &resp.Diagnostics)
	if len(keys) == 0 {
		resp.Diagnostics.AddError("No metric keys", "metric_keys must list at least one metric.")
		return
	}
	component := m.Component.ValueString()
	res, _, err := d.c().Measures.Component(ctx, &sonarqube.MeasuresComponentOptions{
		Component: component, MetricKeys: keys, Branch: m.Branch.ValueString(), PullRequest: m.PullRequest.ValueString()})
	if isGone(err) {
		resp.Diagnostics.AddError("Component not found", explain(err))
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read measures of "+component, err)
		return
	}
	got := map[string]string{}
	if res.Component != nil {
		for _, ms := range res.Component.Measures {
			v := ms.Value
			if v == "" && ms.Period != nil {
				v = ms.Period.Value
			}
			got[ms.Metric] = v
		}
	}
	m.Measures = []measureModel{}
	vals := map[string]attr.Value{}
	for _, k := range keys {
		if v, ok := got[k]; ok {
			m.Measures = append(m.Measures, measureModel{Metric: types.StringValue(k), Value: types.StringValue(v)})
			vals[k] = types.StringValue(v)
		}
	}
	m.Values = types.MapValueMust(types.StringType, vals)
	m.ID = types.StringValue(component)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_permission_templates

type permissionTemplatesDataSource struct{ dataSourceBase }

func newPermissionTemplatesDataSource() datasource.DataSource {
	return &permissionTemplatesDataSource{}
}

type templateDataModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	ProjectKeyPattern types.String `tfsdk:"project_key_pattern"`
}

type permissionTemplatesDataModel struct {
	ID                  types.String        `tfsdk:"id"`
	Search              types.String        `tfsdk:"search"`
	PermissionTemplates []templateDataModel `tfsdk:"permission_templates"`
}

func (d *permissionTemplatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permission_templates"
}

func (d *permissionTemplatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists permission templates, sorted by name.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"search": schema.StringAttribute{Optional: true, Description: "Case-insensitive partial match on the template name."},
			"permission_templates": schema.ListNestedAttribute{Computed: true, Description: "Matching templates.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                  schema.StringAttribute{Computed: true, Description: "Template ID."},
					"name":                schema.StringAttribute{Computed: true, Description: "Template name."},
					"description":         schema.StringAttribute{Computed: true, Description: "Template description."},
					"project_key_pattern": schema.StringAttribute{Computed: true, Description: "Pattern of project keys the template applies to, or null."},
				}}},
		},
	}
}

func (d *permissionTemplatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m permissionTemplatesDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The typed model omits projectKeyPattern.
	var res struct {
		PermissionTemplates []struct {
			ID                string `json:"id"`
			Name              string `json:"name"`
			Description       string `json:"description"`
			ProjectKeyPattern string `json:"projectKeyPattern"`
		} `json:"permissionTemplates"`
	}
	q := map[string][]string{}
	if s := m.Search.ValueString(); s != "" {
		q["q"] = []string{s}
	}
	if _, err := d.c().Do(ctx, &sonarqube.Request{Method: "GET", Path: "/api/permissions/search_templates", Query: q}, &res); err != nil {
		apiError(&resp.Diagnostics, "Unable to list permission templates", err)
		return
	}
	m.PermissionTemplates = []templateDataModel{}
	for _, t := range res.PermissionTemplates {
		m.PermissionTemplates = append(m.PermissionTemplates, templateDataModel{ID: types.StringValue(t.ID), Name: types.StringValue(t.Name),
			Description: types.StringValue(t.Description), ProjectKeyPattern: strOrNull(t.ProjectKeyPattern)})
	}
	slices.SortFunc(m.PermissionTemplates, func(a, b templateDataModel) int { return cmp.Compare(a.Name.ValueString(), b.Name.ValueString()) })
	m.ID = types.StringValue("permission_templates:" + m.Search.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_alm_*: one implementation per platform, derived from the
// resource definitions. Secrets are never returned by SonarQube, so only the
// readable fields are exposed.

type almDataSource struct {
	dataSourceBase
	kind almKind
}

func newAlmDataSource(newResource func() resource.Resource) datasource.DataSource {
	return &almDataSource{kind: newResource().(*almResource).kind}
}

func newAlmGithubDataSource() datasource.DataSource { return newAlmDataSource(newAlmGithubResource) }
func newAlmGitlabDataSource() datasource.DataSource { return newAlmDataSource(newAlmGitlabResource) }
func newAlmAzureDataSource() datasource.DataSource  { return newAlmDataSource(newAlmAzureResource) }
func newAlmBitbucketDataSource() datasource.DataSource {
	return newAlmDataSource(newAlmBitbucketResource)
}
func newAlmBitbucketCloudDataSource() datasource.DataSource {
	return newAlmDataSource(newAlmBitbucketCloudResource)
}

func (d *almDataSource) readable() []almField {
	var out []almField
	for _, f := range d.kind.fields {
		if f.readKey != "" {
			out = append(out, f)
		}
	}
	return out
}

func (d *almDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.kind.typeName
}

func (d *almDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":  schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
		"key": schema.StringAttribute{Required: true, Description: "Key of the setting."},
	}
	for _, f := range d.readable() {
		attrs[f.attr] = schema.StringAttribute{Computed: true, Description: f.desc}
	}
	resp.Schema = schema.Schema{Description: "Looks up a " + d.kind.title + " DevOps platform setting. Its secrets are not readable.", Attributes: attrs}
}

func (d *almDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var key types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("key"), &key)...)
	if resp.Diagnostics.HasError() {
		return
	}
	def, err := almDefinition(ctx, d.c(), d.kind.platform, key.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read DevOps platform settings", err)
		return
	}
	if def == nil {
		resp.Diagnostics.AddError(d.kind.title+" setting not found", fmt.Sprintf("No %s setting has the key %q.", d.kind.title, key.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	for _, f := range d.readable() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(f.attr), strOrNull(str(def[f.readKey])))...)
	}
}
