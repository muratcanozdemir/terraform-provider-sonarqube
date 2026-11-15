package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_application

var _ resource.ResourceWithModifyPlan = (*applicationResource)(nil)

type applicationResource struct{ resourceBase }

func newApplicationResource() resource.Resource { return &applicationResource{} }

type applicationModel struct {
	ID          types.String `tfsdk:"id"`
	Key         types.String `tfsdk:"key"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Visibility  types.String `tfsdk:"visibility"`
	Projects    types.Set    `tfsdk:"projects"`
	Tags        types.Set    `tfsdk:"tags"`
}

func (r *applicationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application"
}

func (r *applicationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "An application, aggregating projects. Requires Developer edition or above.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{projectKeyValidator{}}, Description: "Application key."},
			"name":        schema.StringAttribute{Required: true, Description: "Name.", Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"description": schema.StringAttribute{Optional: true, Description: "Description."},
			"visibility": schema.StringAttribute{Optional: true, Computed: true, Description: "`public` or `private`. Defaults to the server's default.",
				Validators: []validator.String{stringvalidator.OneOf("public", "private")}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"projects": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description: "Keys of the projects in the application. When omitted, membership is not managed.", PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
			"tags": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description: "Tags. When omitted, tags are not managed.", PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *applicationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || !r.configured() {
		return
	}
	r.p.requireEdition(ctx, &resp.Diagnostics, "Applications", "developer")
}

type apiApplication struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Visibility  string   `json:"visibility"`
	Tags        []string `json:"tags"`
	Projects    []struct {
		Key string `json:"key"`
	} `json:"projects"`
}

func (r *applicationResource) show(ctx context.Context, key string) (*apiApplication, error) {
	_, raw, err := r.c().Applications.Show(ctx, &sonarqube.ApplicationsShowOptions{Application: key})
	if err != nil {
		return nil, err
	}
	var v struct {
		Application apiApplication `json:"application"`
	}
	if err := json.Unmarshal(raw.Body, &v); err != nil {
		return nil, err
	}
	return &v.Application, nil
}

func (r *applicationResource) readInto(ctx context.Context, m *applicationModel) (bool, error) {
	a, err := r.show(ctx, m.ID.ValueString())
	if isGone(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m.Key, m.Name = types.StringValue(a.Key), types.StringValue(a.Name)
	m.Description = keepNullIfEmpty(m.Description, a.Description)
	m.Visibility = types.StringValue(a.Visibility)
	var projects []string
	for _, p := range a.Projects {
		projects = append(projects, p.Key)
	}
	m.Projects, m.Tags = stringSet(projects), stringSet(a.Tags)
	return true, nil
}

// sync converges visibility, projects and tags on the plan.
func (r *applicationResource) sync(ctx context.Context, plan, prior *applicationModel, diags *diag.Diagnostics) {
	key := plan.Key.ValueString()
	if !plan.Visibility.IsUnknown() && !plan.Visibility.Equal(prior.Visibility) {
		if _, err := r.c().Projects.UpdateVisibility(ctx, &sonarqube.ProjectsUpdateVisibilityOptions{Project: key, Visibility: plan.Visibility.ValueString()}); err != nil {
			apiError(diags, "Unable to change visibility of application "+key, err)
			return
		}
	}
	if !plan.Projects.IsUnknown() && !plan.Projects.IsNull() {
		add, remove := diffStrings(stringsOf(ctx, prior.Projects, diags), stringsOf(ctx, plan.Projects, diags))
		for _, p := range add {
			if _, err := r.c().Applications.AddProject(ctx, &sonarqube.ApplicationsAddProjectOptions{Application: key, Project: p}); err != nil {
				apiError(diags, fmt.Sprintf("Unable to add %s to application %s", p, key), err)
				return
			}
		}
		for _, p := range remove {
			if _, err := r.c().Applications.RemoveProject(ctx, &sonarqube.ApplicationsRemoveProjectOptions{Application: key, Project: p}); err != nil && !isGone(err) {
				apiError(diags, fmt.Sprintf("Unable to remove %s from application %s", p, key), err)
				return
			}
		}
	}
	if !plan.Tags.IsUnknown() && !plan.Tags.IsNull() && !plan.Tags.Equal(prior.Tags) {
		tags := stringsOf(ctx, plan.Tags, diags)
		if err := rawPost(ctx, r.c(), "/api/applications/set_tags", url.Values{"application": {key}, "tags": {strings.Join(tags, ",")}}); err != nil {
			apiError(diags, "Unable to set tags of application "+key, err)
		}
	}
}

func (r *applicationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan applicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := plan.Key.ValueString()
	_, _, err := r.c().Applications.Create(ctx, &sonarqube.ApplicationsCreateOptions{Key: key, Name: plan.Name.ValueString(),
		Description: plan.Description.ValueString(), Visibility: plan.Visibility.ValueString()})
	if err != nil {
		createError(&resp.Diagnostics, "application", key, err)
		return
	}
	state := applicationModel{ID: types.StringValue(key)}
	if _, err := r.readInto(ctx, &state); err != nil {
		apiError(&resp.Diagnostics, "Unable to read application "+key, err)
		return
	}
	state.Description = plan.Description
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	r.sync(ctx, &plan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.readInto(ctx, &state); err != nil {
		apiError(&resp.Diagnostics, "Unable to read application "+key, err)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *applicationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state applicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.readInto(ctx, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read application "+state.ID.ValueString(), err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *applicationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state applicationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.ID.ValueString()
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		form := url.Values{"application": {key}, "name": {plan.Name.ValueString()}, "description": {plan.Description.ValueString()}}
		if err := rawPost(ctx, r.c(), "/api/applications/update", form); err != nil {
			apiError(&resp.Diagnostics, "Unable to update application "+key, err)
			return
		}
	}
	// Diff against live membership, not state.
	live := state
	if _, err := r.readInto(ctx, &live); err != nil {
		apiError(&resp.Diagnostics, "Unable to read application "+key, err)
		return
	}
	r.sync(ctx, &plan, &live, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.readInto(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to read application "+key, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *applicationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state applicationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c().Applications.Delete(ctx, &sonarqube.ApplicationsDeleteOptions{Application: state.ID.ValueString()}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete application "+state.ID.ValueString(), err)
	}
}

func (r *applicationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// ---------------------------------------------------------------------------
// sonarqube_portfolio

var (
	_ resource.ResourceWithModifyPlan     = (*portfolioResource)(nil)
	_ resource.ResourceWithValidateConfig = (*portfolioResource)(nil)
)

type portfolioResource struct{ resourceBase }

func newPortfolioResource() resource.Resource { return &portfolioResource{} }

type portfolioModel struct {
	ID               types.String `tfsdk:"id"`
	Key              types.String `tfsdk:"key"`
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	Qualifier        types.String `tfsdk:"qualifier"`
	Visibility       types.String `tfsdk:"visibility"`
	SelectionMode    types.String `tfsdk:"selection_mode"`
	Branch           types.String `tfsdk:"branch"`
	Tags             types.List   `tfsdk:"tags"`
	Regexp           types.String `tfsdk:"regexp"`
	SelectedProjects types.Set    `tfsdk:"selected_projects"`
	Applications     types.Set    `tfsdk:"applications"`

	Portfolios          types.Set `tfsdk:"portfolios"`
	ApplicationBranches types.Map `tfsdk:"application_branches"`
}

var applicationBranchesType = types.MapType{ElemType: types.SetType{ElemType: types.StringType}}

var selectedProjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"project_key":       types.StringType,
	"selected_branches": types.SetType{ElemType: types.StringType},
}}

func (r *portfolioResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_portfolio"
}

func (r *portfolioResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A portfolio. Requires Enterprise edition or above.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{projectKeyValidator{}}, Description: "Portfolio key."},
			"name":        schema.StringAttribute{Required: true, Description: "Name.", Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"description": schema.StringAttribute{Optional: true, Description: "Description."},
			"qualifier":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "`VW`."},
			"visibility": schema.StringAttribute{Optional: true, Computed: true, Description: "`public` or `private`; changes in place.",
				Validators: []validator.String{stringvalidator.OneOf("public", "private")}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"selection_mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("NONE"),
				Description: "How projects are selected: `NONE`, `MANUAL`, `TAGS`, `REGEXP` or `REST` (all remaining projects).",
				Validators:  []validator.String{stringvalidator.OneOf("NONE", "MANUAL", "TAGS", "REGEXP", "REST")}},
			"branch": schema.StringAttribute{Optional: true, Description: "Branch to select, for TAGS, REGEXP and REST modes. Defaults to main branches."},
			"tags":   schema.ListAttribute{Optional: true, ElementType: types.StringType, Description: "Tags, for TAGS mode."},
			"regexp": schema.StringAttribute{Optional: true, Description: "Regular expression on project keys, for REGEXP mode."},
			"selected_projects": schema.SetNestedAttribute{Optional: true, Description: "Projects, for MANUAL mode.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"project_key":       schema.StringAttribute{Required: true, Description: "Key of a project to include in the portfolio."},
					"selected_branches": schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Branches. When omitted, the main branch."},
				}}},
			"applications": schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Keys of applications in the portfolio."},
			"portfolios": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				Description: "Keys of other portfolios included in this one as sub-portfolios. They are references: each stays a " +
					"portfolio of its own. SonarQube rejects cycles. When omitted, sub-portfolios are not managed."},
			"application_branches": schema.MapAttribute{Optional: true, ElementType: types.SetType{ElemType: types.StringType},
				Description: "Branches selected for applications of the portfolio, by application key. An application added to " +
					"a portfolio starts with its main branch. Listed applications get exactly these branches (name the main " +
					"branch too to keep it); applications not listed keep their selection. Needs `applications`.",
				Validators: []validator.Map{
					mapvalidator.ValueSetsAre(setvalidator.SizeAtLeast(1)),
					mapvalidator.AlsoRequires(path.MatchRoot("applications")),
				}},
		},
	}
}

func (r *portfolioResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg portfolioModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.SelectionMode.IsUnknown() {
		return
	}
	mode := cfg.SelectionMode.ValueString()
	if mode == "" {
		mode = "NONE"
	}
	check := func(set bool, attrName string, modes ...string) {
		if set && !slices.Contains(modes, mode) {
			resp.Diagnostics.AddAttributeError(path.Root(attrName), "Attribute not used in this selection mode",
				fmt.Sprintf("%s only applies to selection_mode %v, not %s.", attrName, modes, mode))
		}
	}
	check(!cfg.Tags.IsNull(), "tags", "TAGS")
	check(!cfg.Regexp.IsNull(), "regexp", "REGEXP")
	check(!cfg.SelectedProjects.IsNull(), "selected_projects", "MANUAL")
	check(!cfg.Branch.IsNull(), "branch", "TAGS", "REGEXP", "REST")
	if mode == "TAGS" && cfg.Tags.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("tags"), "Missing tags", "selection_mode TAGS needs tags.")
	}
	if mode == "REGEXP" && cfg.Regexp.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("regexp"), "Missing regexp", "selection_mode REGEXP needs regexp.")
	}
	if !cfg.ApplicationBranches.IsNull() && !cfg.ApplicationBranches.IsUnknown() && !cfg.Applications.IsNull() && !cfg.Applications.IsUnknown() {
		apps := stringsOf(ctx, cfg.Applications, &resp.Diagnostics)
		for app, v := range cfg.ApplicationBranches.Elements() {
			if !v.IsUnknown() && !slices.Contains(apps, app) {
				resp.Diagnostics.AddAttributeError(path.Root("application_branches").AtMapKey(app), "Application not in the portfolio",
					fmt.Sprintf("Application %q has branches selected but is not in applications.", app))
			}
		}
	}
}

func (r *portfolioResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || !r.configured() {
		return
	}
	r.p.requireEdition(ctx, &resp.Diagnostics, "Portfolios", "enterprise")
}

type apiPortfolio struct {
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	Desc             string   `json:"desc"`
	Qualifier        string   `json:"qualifier"`
	Visibility       string   `json:"visibility"`
	SelectionMode    string   `json:"selectionMode"`
	Branch           string   `json:"branch"`
	Tags             []string `json:"tags"`
	Regexp           string   `json:"regexp"`
	SelectedProjects []struct {
		ProjectKey       string   `json:"projectKey"`
		SelectedBranches []string `json:"selectedBranches"`
	} `json:"selectedProjects"`
	SubViews []struct {
		Qualifier        string   `json:"qualifier"`
		OriginalKey      string   `json:"originalKey"`
		SelectedBranches []string `json:"selectedBranches"`
	} `json:"subViews"`
}

func (r *portfolioResource) show(ctx context.Context, key string) (*apiPortfolio, error) {
	_, raw, err := r.c().Views.Show(ctx, &sonarqube.ViewsShowOptions{Key: key})
	if err != nil {
		return nil, err
	}
	var v apiPortfolio
	return &v, json.Unmarshal(raw.Body, &v)
}

func (p *apiPortfolio) apps() []string {
	var out []string
	for _, s := range p.SubViews {
		if s.Qualifier == "APP" {
			out = append(out, s.OriginalKey)
		}
	}
	return out
}

// refs returns the keys of the portfolios this one references. Local
// sub-portfolios (without an original key) are not references.
func (p *apiPortfolio) refs() []string {
	var out []string
	for _, s := range p.SubViews {
		if s.Qualifier == "VW" && s.OriginalKey != "" {
			out = append(out, s.OriginalKey)
		}
	}
	return out
}

func (p *apiPortfolio) appBranches() map[string][]string {
	out := map[string][]string{}
	for _, s := range p.SubViews {
		if s.Qualifier == "APP" {
			out[s.OriginalKey] = s.SelectedBranches
		}
	}
	return out
}

func (p *apiPortfolio) manual() map[string][]string {
	out := map[string][]string{}
	for _, s := range p.SelectedProjects {
		out[s.ProjectKey] = s.SelectedBranches
	}
	return out
}

func (r *portfolioResource) readInto(ctx context.Context, m *portfolioModel, diags *diag.Diagnostics) (bool, error) {
	p, err := r.show(ctx, m.ID.ValueString())
	if isGone(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m.Key, m.Name, m.Qualifier = types.StringValue(p.Key), types.StringValue(p.Name), types.StringValue(p.Qualifier)
	m.Description = keepNullIfEmpty(m.Description, p.Desc)
	m.Visibility = types.StringValue(p.Visibility)
	m.SelectionMode = types.StringValue(p.SelectionMode)
	m.Branch = keepNullIfEmpty(m.Branch, p.Branch)
	m.Regexp = keepNullIfEmpty(m.Regexp, p.Regexp)
	if len(p.Tags) > 0 || !m.Tags.IsNull() {
		m.Tags = stringList(p.Tags)
	}
	if len(p.SelectedProjects) > 0 || !m.SelectedProjects.IsNull() {
		// Keep selected_branches null where it was not configured and the
		// server reports no explicit branch (the main branch is implied).
		explicit := map[string]bool{}
		if !m.SelectedProjects.IsNull() && !m.SelectedProjects.IsUnknown() {
			var prior []selectedProject
			diags.Append(m.SelectedProjects.ElementsAs(ctx, &prior, false)...)
			for _, sp := range prior {
				explicit[sp.ProjectKey.ValueString()] = !sp.SelectedBranches.IsNull()
			}
		}
		var elems []attr.Value
		for _, s := range p.SelectedProjects {
			branches := types.SetNull(types.StringType)
			if len(s.SelectedBranches) > 0 || explicit[s.ProjectKey] {
				branches = stringSet(s.SelectedBranches)
			}
			elems = append(elems, types.ObjectValueMust(selectedProjectType.AttrTypes, map[string]attr.Value{
				"project_key": types.StringValue(s.ProjectKey), "selected_branches": branches}))
		}
		set, d := types.SetValue(selectedProjectType, elems)
		diags.Append(d...)
		m.SelectedProjects = set
	}
	if apps := p.apps(); len(apps) > 0 || !m.Applications.IsNull() {
		m.Applications = stringSet(apps)
	}
	if refs := p.refs(); len(refs) > 0 || !m.Portfolios.IsNull() {
		m.Portfolios = stringSet(refs)
	}
	// Only the applications listed in application_branches are managed.
	if !m.ApplicationBranches.IsNull() && !m.ApplicationBranches.IsUnknown() {
		live := p.appBranches()
		out := map[string]attr.Value{}
		for app := range m.ApplicationBranches.Elements() {
			if b, ok := live[app]; ok {
				out[app] = stringSet(b)
			}
		}
		v, d := types.MapValue(applicationBranchesType.ElemType, out)
		diags.Append(d...)
		m.ApplicationBranches = v
	}
	return true, nil
}

type selectedProject struct {
	ProjectKey       types.String `tfsdk:"project_key"`
	SelectedBranches types.Set    `tfsdk:"selected_branches"`
}

// converge applies selection mode, manual selection and applications, diffing
// against the live portfolio.
func (r *portfolioResource) converge(ctx context.Context, plan *portfolioModel, live *apiPortfolio, diags *diag.Diagnostics) {
	key := plan.Key.ValueString()
	c := r.c()
	mode, branch := plan.SelectionMode.ValueString(), plan.Branch.ValueString()
	var err error
	switch mode {
	case "NONE":
		if live.SelectionMode != "NONE" {
			_, err = c.Views.SetNoneMode(ctx, &sonarqube.ViewsSetNoneModeOptions{Portfolio: key})
		}
	case "MANUAL":
		if live.SelectionMode != "MANUAL" {
			_, err = c.Views.SetManualMode(ctx, &sonarqube.ViewsSetManualModeOptions{Portfolio: key})
			live.SelectedProjects = nil
		}
	case "TAGS":
		tags := stringsOf(ctx, plan.Tags, diags)
		if live.SelectionMode != "TAGS" || !sameElements(live.Tags, tags) || live.Branch != branch {
			_, err = c.Views.SetTagsMode(ctx, &sonarqube.ViewsSetTagsModeOptions{Portfolio: key, Tags: tags, Branch: branch})
		}
	case "REGEXP":
		if live.SelectionMode != "REGEXP" || live.Regexp != plan.Regexp.ValueString() || live.Branch != branch {
			_, err = c.Views.SetRegexpMode(ctx, &sonarqube.ViewsSetRegexpModeOptions{Portfolio: key, Regexp: plan.Regexp.ValueString(), Branch: branch})
		}
	case "REST":
		if live.SelectionMode != "REST" || live.Branch != branch {
			_, err = c.Views.SetRemainingProjectsMode(ctx, &sonarqube.ViewsSetRemainingProjectsModeOptions{Portfolio: key, Branch: branch})
		}
	}
	if err != nil {
		apiError(diags, "Unable to set the selection mode of portfolio "+key, err)
		return
	}
	if mode == "MANUAL" && !plan.SelectedProjects.IsUnknown() {
		var want []selectedProject
		if !plan.SelectedProjects.IsNull() {
			diags.Append(plan.SelectedProjects.ElementsAs(ctx, &want, false)...)
		}
		have := live.manual()
		wanted := map[string]bool{}
		for _, sp := range want {
			pk := sp.ProjectKey.ValueString()
			wanted[pk] = true
			if _, ok := have[pk]; !ok {
				if _, err := c.Views.AddProject(ctx, &sonarqube.ViewsAddProjectOptions{Key: key, Project: pk}); err != nil {
					apiError(diags, fmt.Sprintf("Unable to add %s to portfolio %s", pk, key), err)
					return
				}
			}
			if sp.SelectedBranches.IsNull() || sp.SelectedBranches.IsUnknown() {
				continue
			}
			addB, rmB := diffStrings(have[pk], stringsOf(ctx, sp.SelectedBranches, diags))
			for _, b := range addB {
				if _, err := c.Views.AddProjectBranch(ctx, &sonarqube.ViewsAddProjectBranchOptions{Key: key, Project: pk, Branch: b}); err != nil {
					apiError(diags, fmt.Sprintf("Unable to add branch %s of %s to portfolio %s", b, pk, key), err)
					return
				}
			}
			for _, b := range rmB {
				if _, err := c.Views.RemoveProjectBranch(ctx, &sonarqube.ViewsRemoveProjectBranchOptions{Key: key, Project: pk, Branch: b}); err != nil && !isGone(err) {
					apiError(diags, fmt.Sprintf("Unable to remove branch %s of %s from portfolio %s", b, pk, key), err)
					return
				}
			}
		}
		for pk := range have {
			if !wanted[pk] {
				if _, err := c.Views.RemoveProject(ctx, &sonarqube.ViewsRemoveProjectOptions{Key: key, Project: pk}); err != nil && !isGone(err) {
					apiError(diags, fmt.Sprintf("Unable to remove %s from portfolio %s", pk, key), err)
					return
				}
			}
		}
	}
	if !plan.Applications.IsUnknown() && !plan.Applications.IsNull() {
		add, remove := diffStrings(live.apps(), stringsOf(ctx, plan.Applications, diags))
		for _, a := range add {
			if _, err := c.Views.AddApplication(ctx, &sonarqube.ViewsAddApplicationOptions{Portfolio: key, Application: a}); err != nil {
				apiError(diags, fmt.Sprintf("Unable to add application %s to portfolio %s", a, key), err)
				return
			}
		}
		for _, a := range remove {
			if _, err := c.Views.RemoveApplication(ctx, &sonarqube.ViewsRemoveApplicationOptions{Portfolio: key, Application: a}); err != nil && !isGone(err) {
				apiError(diags, fmt.Sprintf("Unable to remove application %s from portfolio %s", a, key), err)
				return
			}
		}
	}
	if !plan.Portfolios.IsUnknown() && !plan.Portfolios.IsNull() {
		add, remove := diffStrings(live.refs(), stringsOf(ctx, plan.Portfolios, diags))
		for _, ref := range add {
			if _, err := c.Views.AddPortfolio(ctx, &sonarqube.ViewsAddPortfolioOptions{Portfolio: key, Reference: ref}); err != nil {
				apiError(diags, fmt.Sprintf("Unable to add portfolio %s to portfolio %s", ref, key), err)
				return
			}
		}
		for _, ref := range remove {
			if _, err := c.Views.RemovePortfolio(ctx, &sonarqube.ViewsRemovePortfolioOptions{Portfolio: key, Reference: ref}); err != nil && !isGone(err) {
				apiError(diags, fmt.Sprintf("Unable to remove portfolio %s from portfolio %s", ref, key), err)
				return
			}
		}
	}
	if !plan.ApplicationBranches.IsUnknown() && !plan.ApplicationBranches.IsNull() {
		// Re-read: applications added above start with their main branch.
		now, err := r.show(ctx, key)
		if err != nil {
			apiError(diags, "Unable to read portfolio "+key, err)
			return
		}
		have := now.appBranches()
		for app, v := range plan.ApplicationBranches.Elements() {
			set, ok := v.(types.Set)
			if !ok || set.IsUnknown() {
				continue
			}
			add, remove := diffStrings(have[app], stringsOf(ctx, set, diags))
			// Add before removing: removing an application's last branch
			// removes the application from the portfolio.
			for _, b := range add {
				if _, err := c.Views.AddApplicationBranch(ctx, &sonarqube.ViewsAddApplicationBranchOptions{Key: key, Application: app, Branch: b}); err != nil {
					apiError(diags, fmt.Sprintf("Unable to select branch %s of application %s in portfolio %s", b, app, key), err)
					return
				}
			}
			for _, b := range remove {
				if _, err := c.Views.RemoveApplicationBranch(ctx, &sonarqube.ViewsRemoveApplicationBranchOptions{Key: key, Application: app, Branch: b}); err != nil && !isGone(err) {
					apiError(diags, fmt.Sprintf("Unable to deselect branch %s of application %s in portfolio %s", b, app, key), err)
					return
				}
			}
		}
	}
}

func (r *portfolioResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan portfolioModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := plan.Key.ValueString()
	_, err := r.c().Views.Create(ctx, &sonarqube.ViewsCreateOptions{Key: key, Name: plan.Name.ValueString(),
		Description: plan.Description.ValueString(), Visibility: plan.Visibility.ValueString()})
	if err != nil {
		createError(&resp.Diagnostics, "portfolio", key, err)
		return
	}
	state := plan
	state.ID, state.Qualifier = types.StringValue(key), types.StringValue("VW")
	if state.Visibility.IsUnknown() {
		state.Visibility = types.StringNull()
	}
	for _, sp := range []*types.Set{&state.SelectedProjects} {
		if sp.IsUnknown() {
			*sp = types.SetNull(selectedProjectType)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	live, err := r.show(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key, err)
		return
	}
	r.converge(ctx, &plan, live, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.readInto(ctx, &state, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key, err)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *portfolioResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state portfolioModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.readInto(ctx, &state, &resp.Diagnostics)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+state.ID.ValueString(), err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *portfolioResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state portfolioModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.ID.ValueString()
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		form := url.Values{"key": {key}, "name": {plan.Name.ValueString()}, "description": {plan.Description.ValueString()}}
		if err := rawPost(ctx, r.c(), "/api/views/update", form); err != nil {
			apiError(&resp.Diagnostics, "Unable to update portfolio "+key, err)
			return
		}
	}
	if !plan.Visibility.IsUnknown() && !plan.Visibility.Equal(state.Visibility) {
		if _, err := r.c().Projects.UpdateVisibility(ctx, &sonarqube.ProjectsUpdateVisibilityOptions{Project: key, Visibility: plan.Visibility.ValueString()}); err != nil {
			apiError(&resp.Diagnostics, "Unable to change visibility of portfolio "+key, err)
			return
		}
	}
	live, err := r.show(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key, err)
		return
	}
	r.converge(ctx, &plan, live, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.readInto(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "Unable to read portfolio "+key, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *portfolioResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state portfolioModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c().Views.Delete(ctx, &sonarqube.ViewsDeleteOptions{Key: state.ID.ValueString()}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete portfolio "+state.ID.ValueString(), err)
	}
}

func (r *portfolioResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *applicationResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *portfolioResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
