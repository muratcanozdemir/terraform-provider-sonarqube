package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_project_branch_retention

type branchRetentionResource struct{ resourceBase }

func newBranchRetentionResource() resource.Resource { return &branchRetentionResource{} }

type branchRetentionModel struct {
	ID      types.String `tfsdk:"id"`
	Project types.String `tfsdk:"project"`
	Branch  types.String `tfsdk:"branch"`
}

func (r *branchRetentionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_branch_retention"
}

func (r *branchRetentionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "Keeps one branch of a project when it becomes inactive (\"Keep when inactive\" in the UI): housekeeping, " +
			"which deletes branches not analyzed for `sonar.dbcleaner.daysBeforeDeletingInactiveBranchesAndPRs` days, skips it. " +
			"Destroying the resource lets housekeeping delete the branch again. The branch must exist, i.e. have been analyzed; " +
			"branches other than the main one need Developer edition or above. The main branch is always kept. To keep the " +
			"branches created from now on whose name matches a pattern, add the pattern to the list setting " +
			"`sonar.dbcleaner.branchesToKeepWhenInactive` (default `main`, `master`, `develop`, `trunk`); patterns do not " +
			"apply to existing branches. Import ID: `project/branch`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "`project/branch`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project key."},
			"branch":  schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Branch name, e.g. `release/1.x`."},
		},
	}
}

// branch returns the project's branch named name, or nil.
func (r *branchRetentionResource) branch(ctx context.Context, project, name string) (*sonarqube.ProjectBranchesListResponseBranch, error) {
	res, _, err := r.c().ProjectBranches.List(ctx, &sonarqube.ProjectBranchesListOptions{Project: project})
	if err != nil {
		return nil, err
	}
	for _, b := range res.Branches {
		if b.Name == name {
			return b, nil
		}
	}
	return nil, nil
}

func (r *branchRetentionResource) protect(ctx context.Context, m *branchRetentionModel, keep bool) error {
	_, err := r.c().ProjectBranches.SetAutomaticDeletionProtection(ctx, &sonarqube.ProjectBranchesSetAutomaticDeletionProtectionOptions{
		Project: m.Project.ValueString(), Branch: m.Branch.ValueString(), Value: &keep})
	return err
}

func (r *branchRetentionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchRetentionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, name := plan.Project.ValueString(), plan.Branch.ValueString()
	b, err := r.branch(ctx, project, name)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list the branches of "+project, err)
		return
	}
	if b == nil {
		resp.Diagnostics.AddAttributeError(path.Root("branch"), "Branch not found",
			fmt.Sprintf("Project %q has no branch %q. Branches are created by their first analysis.", project, name))
		return
	}
	if !b.IsMain { // the main branch is always kept, and the API refuses to change it
		if err := r.protect(ctx, &plan, true); err != nil {
			apiError(&resp.Diagnostics, fmt.Sprintf("Unable to keep branch %s of %s", name, project), err)
			return
		}
	}
	plan.ID = types.StringValue(project + "/" + name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *branchRetentionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state branchRetentionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.branch(ctx, state.Project.ValueString(), state.Branch.ValueString())
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list the branches of "+state.Project.ValueString(), err)
		return
	}
	// A deleted branch, or one no longer kept (changed outside Terraform).
	if b == nil || (!b.ExcludedFromPurge && !b.IsMain) {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID = types.StringValue(state.Project.ValueString() + "/" + state.Branch.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *branchRetentionResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute requires replacement.
}

func (r *branchRetentionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state branchRetentionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.branch(ctx, state.Project.ValueString(), state.Branch.ValueString())
	if err == nil && b != nil && !b.IsMain {
		err = r.protect(ctx, &state, false)
	}
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, fmt.Sprintf("Unable to stop keeping branch %s of %s", state.Branch.ValueString(), state.Project.ValueString()), err)
	}
}

// ImportState accepts `project/branch`. Project keys cannot contain a slash,
// so the branch name may.
func (r *branchRetentionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, "/", 2, "project/branch")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("branch"), parts[1])...)
}

func (r *branchRetentionResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
