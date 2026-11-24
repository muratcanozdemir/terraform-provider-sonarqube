package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
// sonarqube_group

var (
	_ resource.ResourceWithModifyPlan = (*groupResource)(nil)
	_ resource.ResourceWithModifyPlan = (*groupMemberResource)(nil)
)

type groupResource struct{ resourceBase }

// ModifyPlan refuses to create or change a group on an externally managed
// instance. Deleting one is allowed there.
func (r *groupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	create, update := planChange(&r.resourceBase, req)
	if !create && !update {
		return
	}
	var name types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	verb := "Changing"
	if create {
		verb = "Creating"
	}
	r.p.refuseIfManaged(ctx, &resp.Diagnostics, fmt.Sprintf("%s group %q", verb, name.ValueString()))
}

func newGroupResource() resource.Resource { return &groupResource{} }

type groupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A SonarQube user group. Renaming a group updates it in place and keeps its members and permissions." +
			" On an instance with GitHub, GitLab or SCIM `AUTO_PROVISIONING`, SonarQube refuses to create or change groups, so such plans fail; deleting a group is still allowed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Group UUID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, Description: "Group name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255), noSurroundingSpace{}}},
			"description": schema.StringAttribute{Optional: true, Description: "Group description.",
				Validators: []validator.String{stringvalidator.LengthAtMost(200)}},
		},
	}
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := &sonarqube.GroupCreateRestRequest{Name: plan.Name.ValueString()}
	if !plan.Description.IsNull() {
		body.Description = new(plan.Description.ValueString())
	}
	g, _, err := r.c().Authorizations.CreateGroup(ctx, &sonarqube.AuthorizationsCreateGroupOptions{Body: body})
	if err != nil {
		createError(&resp.Diagnostics, "group", plan.Name.ValueString(), err)
		return
	}
	plan.ID = types.StringValue(g.ID)
	plan.Description = keepNullIfEmpty(plan.Description, g.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, _, err := r.c().Authorizations.GetGroup(ctx, &sonarqube.AuthorizationsGetGroupOptions{ID: state.ID.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read group "+state.Name.ValueString(), err)
		return
	}
	state.Name = types.StringValue(g.Name)
	state.Description = keepNullIfEmpty(state.Description, g.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := &sonarqube.GroupUpdateRestRequest{}
	if !plan.Name.Equal(state.Name) {
		body.Name = new(plan.Name.ValueString())
	}
	if !plan.Description.Equal(state.Description) {
		body.Description = new(plan.Description.ValueString()) // "" clears
	}
	g, _, err := r.c().Authorizations.UpdateGroup(ctx, &sonarqube.AuthorizationsUpdateGroupOptions{ID: state.ID.ValueString(), Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to update group "+state.Name.ValueString(), err)
		return
	}
	plan.ID = state.ID
	plan.Name = types.StringValue(g.Name)
	plan.Description = keepNullIfEmpty(plan.Description, g.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().Authorizations.DeleteGroup(ctx, &sonarqube.AuthorizationsDeleteGroupOptions{ID: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete group "+state.Name.ValueString(), err)
	}
}

// ImportState accepts the group UUID or its exact name.
func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	g, err := findGroup(ctx, r.c(), req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to import group "+req.ID, err)
		return
	}
	if g == nil {
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("No group has the UUID or name %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), g.ID)...)
}

// findGroup looks a group up by UUID, then by exact name. It returns nil
// when neither matches.
func findGroup(ctx context.Context, c *sonarqube.Client, idOrName string) (*sonarqube.GroupRestResponse, error) {
	g, _, err := c.Authorizations.GetGroup(ctx, &sonarqube.AuthorizationsGetGroupOptions{ID: idOrName})
	if err == nil {
		return g, nil
	}
	// A name is not a valid UUID; servers answer 404 or 400 for it.
	if !isGone(err) && sonarqube.StatusCode(err) != 400 {
		return nil, err
	}
	return findGroupByName(ctx, c, idOrName)
}

func findGroupByName(ctx context.Context, c *sonarqube.Client, name string) (*sonarqube.GroupRestResponse, error) {
	groups, err := collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.GroupRestResponse, int64, error) {
		res, _, err := c.Authorizations.SearchGroups(ctx, &sonarqube.AuthorizationsSearchGroupsOptions{Q: name, PageIndex: &page, PageSize: &size})
		if err != nil {
			return nil, 0, err
		}
		var t int64
		if res.Page != nil {
			t = int64(res.Page.Total)
		}
		return res.Groups, t, nil
	})
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Name == name {
			return g, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// sonarqube_group_member

type groupMemberResource struct{ resourceBase }

func newGroupMemberResource() resource.Resource { return &groupMemberResource{} }

type groupMemberModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	LoginName types.String `tfsdk:"login_name"`
}

func (r *groupMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_member"
}

func (r *groupMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "Membership of one user in one group. Import ID: `group[login]`." +
			" On an instance with GitHub, GitLab or SCIM `AUTO_PROVISIONING`, SonarQube refuses to add or remove members, so adding one fails at plan time, and destroying one only removes it from state, with a warning.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":       schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Group name."},
			"login_name": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "User login."},
		},
	}
}

func groupMemberID(group, login string) string { return group + "[" + login + "]" }

// membership resolves the group and user to their UUIDs and returns the ID
// of the user's membership in the group, or "" when there is none. A missing
// group or user returns ok=false.
func (r *groupMemberResource) membership(ctx context.Context, group, login string) (groupID, userID, id string, ok bool, err error) {
	g, err := findGroupByName(ctx, r.c(), group)
	if err != nil || g == nil {
		return "", "", "", false, err
	}
	u, err := findUser(ctx, r.c(), "", login)
	if err != nil || u == nil {
		return g.ID, "", "", false, err
	}
	one := 1
	res, _, err := r.c().Authorizations.SearchGroupMemberships(ctx, &sonarqube.AuthorizationsSearchGroupMembershipsOptions{
		GroupID: g.ID, UserID: u.ID, PageSize: &one})
	if err != nil {
		return g.ID, u.ID, "", false, err
	}
	for _, m := range res.GroupMemberships {
		if m.GroupID == g.ID && m.UserID == u.ID {
			return g.ID, u.ID, m.ID, true, nil
		}
	}
	return g.ID, u.ID, "", true, nil
}

func (r *groupMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	group, login := plan.Name.ValueString(), plan.LoginName.ValueString()
	groupID, userID, id, ok, err := r.membership(ctx, group, login)
	if err != nil {
		apiError(&resp.Diagnostics, fmt.Sprintf("Unable to look up group %s and user %s", group, login), err)
		return
	}
	switch {
	case groupID == "":
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("No group is named %q.", group))
		return
	case !ok:
		resp.Diagnostics.AddError("User not found", fmt.Sprintf("No active user has the login %q.", login))
		return
	case id == "":
		_, _, err := r.c().Authorizations.CreateGroupMembership(ctx, &sonarqube.AuthorizationsCreateGroupMembershipOptions{
			Body: &sonarqube.GroupMembershipCreateRestRequest{GroupID: &groupID, UserID: &userID}})
		if err != nil {
			apiError(&resp.Diagnostics, fmt.Sprintf("Unable to add %s to group %s", login, group), err)
			return
		}
	}
	// Already a member (e.g. the default group): adopting the membership is
	// what the configuration asks for.
	plan.ID = types.StringValue(groupMemberID(group, login))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, _, id, _, err := r.membership(ctx, state.Name.ValueString(), state.LoginName.ValueString())
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to read members of group "+state.Name.ValueString(), err)
		return
	}
	if id == "" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID = types.StringValue(groupMemberID(state.Name.ValueString(), state.LoginName.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ModifyPlan refuses to add a member on an externally managed instance.
func (r *groupMemberResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	create, update := planChange(&r.resourceBase, req)
	if !create && !update {
		return
	}
	var plan groupMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	r.p.refuseIfManaged(ctx, &resp.Diagnostics, fmt.Sprintf("Adding %q to group %q", plan.LoginName.ValueString(), plan.Name.ValueString()))
}

func (r *groupMemberResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// Every attribute requires replacement.
}

func (r *groupMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, _, id, _, err := r.membership(ctx, state.Name.ValueString(), state.LoginName.ValueString())
	if err == nil && id != "" {
		_, err = r.c().Authorizations.DeleteGroupMembership(ctx, &sonarqube.AuthorizationsDeleteGroupMembershipOptions{ID: id})
	}
	if isExternallyManaged(err) {
		// The identity provider owns memberships now: stop managing this one
		// rather than fail every destroy.
		resp.Diagnostics.AddWarning("Group membership left in place",
			fmt.Sprintf("SonarQube is externally managed and refuses to remove %q from group %q; the membership is now synchronized "+
				"from the identity provider. It was removed from Terraform state only.", state.LoginName.ValueString(), state.Name.ValueString()))
		return
	}
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to remove group member", err)
	}
}

func (r *groupMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Split at the last '[': a login cannot contain one, a group name can.
	i := strings.LastIndex(req.ID, "[")
	ok := i >= 0
	group, rest := req.ID, ""
	if ok {
		group, rest = req.ID[:i], req.ID[i+1:]
	}
	login, closed := strings.CutSuffix(rest, "]")
	if !ok || !closed || group == "" || login == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected `group[login]`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), group)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("login_name"), login)...)
}

func (r *groupResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *groupMemberResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
