package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// delegationResource grants one user or group the right to edit one
// quality gate or quality profile. The two differ only in how the target is
// addressed, which delegationKind abstracts.
type delegationResource struct {
	resourceBase
	kind delegationKind
}

type delegationTarget struct {
	Name     string // gate or profile name
	Language string // profiles only
}

type delegationKind struct {
	typeName    string
	description string
	targetAttr  string // "gatename" or "profile_name"
	hasLanguage bool
	add         func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error
	remove      func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error
	has         func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) (bool, error)
}

type delegationModel struct {
	ID        types.String `tfsdk:"id"`
	LoginName types.String `tfsdk:"login_name"`
	GroupName types.String `tfsdk:"group_name"`
	Target    types.String `tfsdk:"-"`
	Language  types.String `tfsdk:"-"`
}

func (r *delegationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.kind.typeName
}

func (r *delegationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	principal := []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("login_name"), path.MatchRoot("group_name"))}
	attrs := map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"login_name":      schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: principal, Description: "User to grant. Conflicts with group_name."},
		"group_name":      schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: principal, Description: "Group to grant. Conflicts with login_name."},
		r.kind.targetAttr: schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Name of the " + r.kind.description + "."},
	}
	if r.kind.hasLanguage {
		attrs["language"] = schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Language of the profile."}
	}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Allows a user or group to edit a " + r.kind.description + ". Import ID: " + r.importFormat() + ".",
		Attributes:  attrs,
	}
}

func (r *delegationResource) importFormat() string {
	if r.kind.hasLanguage {
		return "`language/profile[user/login]` or `language/profile[group/name]`"
	}
	return "`gate[user/login]` or `gate[group/name]`"
}

// load reads the model from a plan or state, including the per-kind target
// attributes that the struct tags cannot name statically.
func (r *delegationResource) load(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, m *delegationModel) diag.Diagnostics {
	var d diag.Diagnostics
	d.Append(get(ctx, path.Root("id"), &m.ID)...)
	d.Append(get(ctx, path.Root("login_name"), &m.LoginName)...)
	d.Append(get(ctx, path.Root("group_name"), &m.GroupName)...)
	d.Append(get(ctx, path.Root(r.kind.targetAttr), &m.Target)...)
	m.Language = types.StringNull()
	if r.kind.hasLanguage {
		d.Append(get(ctx, path.Root("language"), &m.Language)...)
	}
	return d
}

func (r *delegationResource) store(ctx context.Context, set func(context.Context, path.Path, any) diag.Diagnostics, m *delegationModel) diag.Diagnostics {
	var d diag.Diagnostics
	d.Append(set(ctx, path.Root("id"), m.ID)...)
	d.Append(set(ctx, path.Root("login_name"), m.LoginName)...)
	d.Append(set(ctx, path.Root("group_name"), m.GroupName)...)
	d.Append(set(ctx, path.Root(r.kind.targetAttr), m.Target)...)
	if r.kind.hasLanguage {
		d.Append(set(ctx, path.Root("language"), m.Language)...)
	}
	return d
}

func (r *delegationResource) id(m *delegationModel) string {
	kind, who := "user", m.LoginName.ValueString()
	if !m.GroupName.IsNull() {
		kind, who = "group", m.GroupName.ValueString()
	}
	id := m.Target.ValueString() + "[" + kind + "/" + who + "]"
	if r.kind.hasLanguage {
		id = m.Language.ValueString() + "/" + id
	}
	return id
}

func (r *delegationResource) target(m *delegationModel) delegationTarget {
	return delegationTarget{Name: m.Target.ValueString(), Language: m.Language.ValueString()}
}

func (r *delegationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m delegationModel
	resp.Diagnostics.Append(r.load(ctx, req.Plan.GetAttribute, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.kind.add(ctx, r.c(), r.target(&m), m.LoginName.ValueString(), m.GroupName.ValueString()); err != nil {
		apiError(&resp.Diagnostics, "Unable to grant access to "+m.Target.ValueString(), err)
		return
	}
	m.ID = types.StringValue(r.id(&m))
	resp.Diagnostics.Append(r.store(ctx, resp.State.SetAttribute, &m)...)
}

func (r *delegationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m delegationModel
	resp.Diagnostics.Append(r.load(ctx, req.State.GetAttribute, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.kind.has(ctx, r.c(), r.target(&m), m.LoginName.ValueString(), m.GroupName.ValueString())
	if isGone(err) || (err == nil && !ok) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read access to "+m.Target.ValueString(), err)
		return
	}
	m.ID = types.StringValue(r.id(&m))
	resp.Diagnostics.Append(r.store(ctx, resp.State.SetAttribute, &m)...)
}

func (r *delegationResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *delegationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m delegationModel
	resp.Diagnostics.Append(r.load(ctx, req.State.GetAttribute, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.kind.remove(ctx, r.c(), r.target(&m), m.LoginName.ValueString(), m.GroupName.ValueString())
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to revoke access to "+m.Target.ValueString(), err)
	}
}

var delegationIDRe = regexp.MustCompile(`^(?:([^/]+)/)?(.+)\[(user|group)/(.+)\]$`)

func (r *delegationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	g := delegationIDRe.FindStringSubmatch(req.ID)
	if g == nil || (r.kind.hasLanguage && g[1] == "") || (!r.kind.hasLanguage && g[1] != "") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected %s, got %q.", r.importFormat(), req.ID))
		return
	}
	m := delegationModel{ID: types.StringValue(req.ID), Target: types.StringValue(g[2]),
		LoginName: types.StringNull(), GroupName: types.StringNull(), Language: types.StringValue(g[1])}
	if g[3] == "user" {
		m.LoginName = types.StringValue(g[4])
	} else {
		m.GroupName = types.StringValue(g[4])
	}
	resp.Diagnostics.Append(r.store(ctx, resp.State.SetAttribute, &m)...)
}

// selectedContains pages through a "selected" search and reports whether
// name is among the results.
func selectedContains[T any](ctx context.Context, name string, fetch func(ctx context.Context, page, size int) ([]T, int64, error), key func(T) string) (bool, error) {
	items, err := collect(ctx, 500, fetch)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if key(it) == name {
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Instances

func newQualityGateUsergroupAssociationResource() resource.Resource {
	return &delegationResource{kind: delegationKind{
		typeName: "_qualitygate_usergroup_association", description: "quality gate", targetAttr: "gatename",
		add: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error {
			if user != "" {
				_, err := c.Qualitygates.AddUser(ctx, &sonarqube.QualitygatesAddUserOptions{GateName: t.Name, Login: user})
				return err
			}
			_, err := c.Qualitygates.AddGroup(ctx, &sonarqube.QualitygatesAddGroupOptions{GateName: t.Name, GroupName: group})
			return err
		},
		remove: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error {
			if user != "" {
				_, err := c.Qualitygates.RemoveUser(ctx, &sonarqube.QualitygatesRemoveUserOptions{GateName: t.Name, Login: user})
				return err
			}
			_, err := c.Qualitygates.RemoveGroup(ctx, &sonarqube.QualitygatesRemoveGroupOptions{GateName: t.Name, GroupName: group})
			return err
		},
		has: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) (bool, error) {
			if user != "" {
				return selectedContains(ctx, user, func(ctx context.Context, p, ps int) ([]*sonarqube.QualitygatesSearchUsersResponseUser, int64, error) {
					res, _, err := c.Qualitygates.SearchUsers(ctx, &sonarqube.QualitygatesSearchUsersOptions{GateName: t.Name, Q: user, Selected: "selected", P: &p, Ps: &ps})
					if err != nil {
						return nil, 0, err
					}
					return res.Users, total(res.Paging), nil
				}, func(u *sonarqube.QualitygatesSearchUsersResponseUser) string { return u.Login })
			}
			return selectedContains(ctx, group, func(ctx context.Context, p, ps int) ([]*sonarqube.QualitygatesSearchGroupsResponseGroup, int64, error) {
				res, _, err := c.Qualitygates.SearchGroups(ctx, &sonarqube.QualitygatesSearchGroupsOptions{GateName: t.Name, Q: group, Selected: "selected", P: &p, Ps: &ps})
				if err != nil {
					return nil, 0, err
				}
				return res.Groups, total(res.Paging), nil
			}, func(g *sonarqube.QualitygatesSearchGroupsResponseGroup) string { return g.Name })
		},
	}}
}

func newQualityProfileUsergroupAssociationResource() resource.Resource {
	return &delegationResource{kind: delegationKind{
		typeName: "_qualityprofile_usergroup_association", description: "quality profile", targetAttr: "profile_name", hasLanguage: true,
		add: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error {
			if user != "" {
				_, err := c.Qualityprofiles.AddUser(ctx, &sonarqube.QualityprofilesAddUserOptions{QualityProfile: t.Name, Language: t.Language, Login: user})
				return err
			}
			_, err := c.Qualityprofiles.AddGroup(ctx, &sonarqube.QualityprofilesAddGroupOptions{QualityProfile: t.Name, Language: t.Language, Group: group})
			return err
		},
		remove: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) error {
			if user != "" {
				_, err := c.Qualityprofiles.RemoveUser(ctx, &sonarqube.QualityprofilesRemoveUserOptions{QualityProfile: t.Name, Language: t.Language, Login: user})
				return err
			}
			_, err := c.Qualityprofiles.RemoveGroup(ctx, &sonarqube.QualityprofilesRemoveGroupOptions{QualityProfile: t.Name, Language: t.Language, Group: group})
			return err
		},
		has: func(ctx context.Context, c *sonarqube.Client, t delegationTarget, user, group string) (bool, error) {
			if user != "" {
				return selectedContains(ctx, user, func(ctx context.Context, p, ps int) ([]*sonarqube.QualityprofilesSearchUsersResponseUser, int64, error) {
					res, _, err := c.Qualityprofiles.SearchUsers(ctx, &sonarqube.QualityprofilesSearchUsersOptions{QualityProfile: t.Name, Language: t.Language, Q: user, Selected: "selected", P: &p, Ps: &ps})
					if err != nil {
						return nil, 0, err
					}
					return res.Users, total(res.Paging), nil
				}, func(u *sonarqube.QualityprofilesSearchUsersResponseUser) string { return u.Login })
			}
			return selectedContains(ctx, group, func(ctx context.Context, p, ps int) ([]*sonarqube.QualityprofilesSearchGroupsResponseGroup, int64, error) {
				res, _, err := c.Qualityprofiles.SearchGroups(ctx, &sonarqube.QualityprofilesSearchGroupsOptions{QualityProfile: t.Name, Language: t.Language, Q: group, Selected: "selected", P: &p, Ps: &ps})
				if err != nil {
					return nil, 0, err
				}
				return res.Groups, total(res.Paging), nil
			}, func(g *sonarqube.QualityprofilesSearchGroupsResponseGroup) string { return g.Name })
		},
	}}
}

func (r *delegationResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
