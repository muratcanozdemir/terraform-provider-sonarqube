package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_application_branch

var (
	_ resource.ResourceWithModifyPlan  = (*applicationBranchResource)(nil)
	_ resource.ResourceWithImportState = (*applicationBranchResource)(nil)
)

type applicationBranchResource struct{ resourceBase }

func newApplicationBranchResource() resource.Resource { return &applicationBranchResource{} }

type applicationBranchModel struct {
	ID          types.String `tfsdk:"id"`
	Application types.String `tfsdk:"application"`
	Name        types.String `tfsdk:"name"`
	Projects    types.Map    `tfsdk:"projects"`
}

func (r *applicationBranchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application_branch"
}

func (r *applicationBranchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A branch of an application: a named combination of branches of the application's projects, " +
			"e.g. a release. Requires Developer edition or above. Renaming changes it in place. " +
			"Import ID: `application/branch`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "`application/branch`."},
			"application": schema.StringAttribute{Required: true, Description: "Application key.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"name": schema.StringAttribute{Required: true, Description: "Branch name. It cannot be the name of the application's main branch.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
			"projects": schema.MapAttribute{Required: true, ElementType: types.StringType,
				Description: "Branch of each project to include, by project key: a branch name, or `\"\"` for the project's main " +
					"branch. The projects must belong to the application; projects of the application that are not " +
					"listed are left out of this branch.",
				Validators: []validator.Map{mapvalidator.SizeAtLeast(1)}},
		},
	}
}

// ModifyPlan derives the ID from the planned application and name (known
// unless they come from unknown values), and checks the edition on create.
func (r *applicationBranchResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var app, name types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("application"), &app)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	if !app.IsUnknown() && !name.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), app.ValueString()+"/"+name.ValueString())...)
	}
	if req.State.Raw.IsNull() && r.configured() {
		r.p.requireEdition(ctx, &resp.Diagnostics, "Application branches", "developer")
	}
}

// selection returns the project keys and branches to send, sorted by key.
func (m *applicationBranchModel) selection(ctx context.Context, diags *diag.Diagnostics) (projects, branches []string) {
	var sel map[string]string
	diags.Append(m.Projects.ElementsAs(ctx, &sel, false)...)
	for k := range sel {
		projects = append(projects, k)
	}
	slices.Sort(projects)
	for _, k := range projects {
		branches = append(branches, sel[k])
	}
	return projects, branches
}

func (r *applicationBranchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan applicationBranchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projects, branches := plan.selection(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	app, name := plan.Application.ValueString(), plan.Name.ValueString()
	if _, err := r.c().Applications.CreateBranch(ctx, &sonarqube.ApplicationsCreateBranchOptions{
		Application: app, Branch: name, Project: projects, ProjectBranch: branches}); err != nil {
		createError(&resp.Diagnostics, "application branch", app+"/"+name, err)
		return
	}
	plan.ID = types.StringValue(app + "/" + name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *applicationBranchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state applicationBranchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, name := state.Application.ValueString(), state.Name.ValueString()
	res, _, err := r.c().Applications.Show(ctx, &sonarqube.ApplicationsShowOptions{Application: app, Branch: name})
	if isGone(err) || (err == nil && (res.Application == nil || res.Application.Branch != name)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read branch "+name+" of application "+app, err)
		return
	}
	var prior map[string]string
	if !state.Projects.IsNull() && !state.Projects.IsUnknown() {
		resp.Diagnostics.Append(state.Projects.ElementsAs(ctx, &prior, false)...)
	}
	sel := map[string]attr.Value{}
	for _, p := range res.Application.Projects {
		if p == nil || !p.Selected {
			continue
		}
		b := p.Branch
		// "" selects the main branch; keep that form while the server
		// still reports the main branch for the project.
		if v, ok := prior[p.Key]; ok && v == "" && p.IsMain {
			b = ""
		}
		sel[p.Key] = types.StringValue(b)
	}
	m, d := types.MapValue(types.StringType, sel)
	resp.Diagnostics.Append(d...)
	state.Projects = m
	state.ID = types.StringValue(app + "/" + name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *applicationBranchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state applicationBranchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projects, branches := plan.selection(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	app := plan.Application.ValueString()
	if _, err := r.c().Applications.UpdateBranch(ctx, &sonarqube.ApplicationsUpdateBranchOptions{
		Application: app, Branch: state.Name.ValueString(), Name: plan.Name.ValueString(),
		Project: projects, ProjectBranch: branches}); err != nil {
		apiError(&resp.Diagnostics, "Unable to update branch "+state.Name.ValueString()+" of application "+app, err)
		return
	}
	plan.ID = types.StringValue(app + "/" + plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *applicationBranchResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state applicationBranchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().Applications.DeleteBranch(ctx, &sonarqube.ApplicationsDeleteBranchOptions{
		Application: state.Application.ValueString(), Branch: state.Name.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete branch "+state.Name.ValueString()+" of application "+state.Application.ValueString(), err)
	}
}

// ImportState accepts `application/branch`. Branch names may contain "/",
// application keys may not, so the first "/" separates them.
func (r *applicationBranchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	app, name, ok := strings.Cut(req.ID, "/")
	if !ok || app == "" || name == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected `application/branch`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &applicationBranchModel{ID: types.StringValue(req.ID),
		Application: types.StringValue(app), Name: types.StringValue(name), Projects: types.MapNull(types.StringType)})...)
}

func (r *applicationBranchResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
