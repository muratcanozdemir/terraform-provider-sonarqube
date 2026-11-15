package provider

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// Data sources for groups, users and tokens. Every list is read across all
// pages and sorted, so that the result does not reorder between plans.

func searchGroups(ctx context.Context, c *sonarqube.Client, q string) ([]*sonarqube.GroupRestResponse, error) {
	return collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.GroupRestResponse, int64, error) {
		res, _, err := c.Authorizations.SearchGroups(ctx, &sonarqube.AuthorizationsSearchGroupsOptions{Q: q, PageIndex: &page, PageSize: &size})
		if err != nil {
			return nil, 0, err
		}
		var t int64
		if res.Page != nil {
			t = int64(res.Page.Total)
		}
		return res.Groups, t, nil
	})
}

func searchUsers(ctx context.Context, c *sonarqube.Client, opts sonarqube.UsersManagementSearchOptions) ([]*sonarqube.UserRestResponseForAdmins, error) {
	opts.Active = ptr(true)
	return collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.UserRestResponseForAdmins, int64, error) {
		o := opts
		o.PageIndex, o.PageSize = &page, &size
		res, _, err := c.UsersManagement.Search(ctx, &o)
		if err != nil {
			return nil, 0, err
		}
		var t int64
		if res.Page != nil {
			t = int64(res.Page.Total)
		}
		return res.Users, t, nil
	})
}

// ---------------------------------------------------------------------------
// sonarqube_group

type groupDataSource struct{ dataSourceBase }

func newGroupDataSource() datasource.DataSource { return &groupDataSource{} }

type groupDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Managed     types.Bool   `tfsdk:"managed"`
	Default     types.Bool   `tfsdk:"default"`
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a group by its exact name.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Group UUID."},
			"name":        schema.StringAttribute{Required: true, Description: "Exact group name."},
			"description": schema.StringAttribute{Computed: true, Description: "Group description."},
			"managed":     schema.BoolAttribute{Computed: true, Description: "Whether the group is provisioned by an external system (SCIM, GitHub, GitLab)."},
			"default":     schema.BoolAttribute{Computed: true, Description: "Whether every user is a member (the built-in `sonar-users` group)."},
		},
	}
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := findGroupByName(ctx, d.c(), m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read group "+m.Name.ValueString(), err)
		return
	}
	if g == nil {
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("No group is named %q. Group names are case-sensitive.", m.Name.ValueString()))
		return
	}
	m.ID, m.Description = types.StringValue(g.ID), types.StringValue(g.Description)
	m.Managed, m.Default = types.BoolValue(g.Managed), types.BoolValue(g.Default)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_groups

type groupsDataSource struct{ dataSourceBase }

func newGroupsDataSource() datasource.DataSource { return &groupsDataSource{} }

type groupsDataModel struct {
	ID     types.String     `tfsdk:"id"`
	Search types.String     `tfsdk:"search"`
	Groups []groupDataModel `tfsdk:"groups"`
}

func (d *groupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_groups"
}

func (d *groupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists groups, sorted by name.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"search": schema.StringAttribute{Optional: true, Description: "Case-insensitive partial match on the group name. Omit to list every group."},
			"groups": schema.ListNestedAttribute{Computed: true, Description: "Matching groups.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.StringAttribute{Computed: true, Description: "Group UUID."},
					"name":        schema.StringAttribute{Computed: true, Description: "Group name."},
					"description": schema.StringAttribute{Computed: true, Description: "Group description."},
					"managed":     schema.BoolAttribute{Computed: true, Description: "Whether the group is provisioned by an external identity provider (GitHub, GitLab or SCIM) and cannot be changed in SonarQube."},
					"default":     schema.BoolAttribute{Computed: true, Description: "Whether this is the default group that every new user joins."},
				}}},
		},
	}
}

func (d *groupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groups, err := searchGroups(ctx, d.c(), m.Search.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list groups", err)
		return
	}
	slices.SortFunc(groups, func(a, b *sonarqube.GroupRestResponse) int { return cmp.Compare(a.Name, b.Name) })
	m.Groups = make([]groupDataModel, 0, len(groups))
	for _, g := range groups {
		m.Groups = append(m.Groups, groupDataModel{ID: types.StringValue(g.ID), Name: types.StringValue(g.Name),
			Description: types.StringValue(g.Description), Managed: types.BoolValue(g.Managed), Default: types.BoolValue(g.Default)})
	}
	m.ID = types.StringValue("groups:" + m.Search.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_group_members

type groupMembersDataSource struct{ dataSourceBase }

func newGroupMembersDataSource() datasource.DataSource { return &groupMembersDataSource{} }

type memberModel struct {
	LoginName types.String `tfsdk:"login_name"`
	Name      types.String `tfsdk:"name"`
}

type groupMembersDataModel struct {
	ID            types.String  `tfsdk:"id"`
	Group         types.String  `tfsdk:"group"`
	LoginName     types.String  `tfsdk:"login_name"`
	IgnoreMissing types.Bool    `tfsdk:"ignore_missing"`
	Members       []memberModel `tfsdk:"members"`
}

func (d *groupMembersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_members"
}

func (d *groupMembersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the active members of a group, sorted by login.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"group":          schema.StringAttribute{Required: true, Description: "Exact group name."},
			"login_name":     schema.StringAttribute{Optional: true, Description: "Only return this user (exact login), e.g. to test membership."},
			"ignore_missing": schema.BoolAttribute{Optional: true, Description: "Return no members, rather than fail, when the group does not exist."},
			"members": schema.ListNestedAttribute{Computed: true, Description: "Group members.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"login_name": schema.StringAttribute{Computed: true, Description: "User login."},
					"name":       schema.StringAttribute{Computed: true, Description: "User display name."},
				}}},
		},
	}
}

func (d *groupMembersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupMembersDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name, login := m.Group.ValueString(), m.LoginName.ValueString()
	m.ID = types.StringValue(name + "/" + login)
	m.Members = []memberModel{}
	g, err := findGroupByName(ctx, d.c(), name)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read group "+name, err)
		return
	}
	if g == nil {
		if !m.IgnoreMissing.ValueBool() {
			resp.Diagnostics.AddError("Group not found", fmt.Sprintf("No group is named %q. Set ignore_missing = true to return no members instead.", name))
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	users, err := searchUsers(ctx, d.c(), sonarqube.UsersManagementSearchOptions{GroupID: g.ID, Q: login})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list members of group "+name, err)
		return
	}
	slices.SortFunc(users, func(a, b *sonarqube.UserRestResponseForAdmins) int { return cmp.Compare(a.Login, b.Login) })
	for _, u := range users {
		if login != "" && u.Login != login {
			continue // q is a partial match on login, name and email
		}
		m.Members = append(m.Members, memberModel{LoginName: types.StringValue(u.Login), Name: types.StringValue(u.Name)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_user

type userDataSource struct{ dataSourceBase }

func newUserDataSource() datasource.DataSource { return &userDataSource{} }

type userDataModel struct {
	ID          types.String `tfsdk:"id"`
	UUID        types.String `tfsdk:"uuid"`
	LoginName   types.String `tfsdk:"login_name"`
	Name        types.String `tfsdk:"name"`
	Email       types.String `tfsdk:"email"`
	IsLocal     types.Bool   `tfsdk:"is_local"`
	Managed     types.Bool   `tfsdk:"managed"`
	SCMAccounts types.Set    `tfsdk:"scm_accounts"`
}

func userData(u *sonarqube.UserRestResponseForAdmins) userDataModel {
	return userDataModel{
		ID: types.StringValue(u.Login), UUID: types.StringValue(u.ID), LoginName: types.StringValue(u.Login),
		Name: types.StringValue(u.Name), Email: types.StringValue(u.Email), IsLocal: types.BoolValue(u.Local),
		Managed: types.BoolValue(u.Managed), SCMAccounts: stringSet(u.SCMAccounts),
	}
}

func userAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":           schema.StringAttribute{Computed: true, Description: "The login."},
		"uuid":         schema.StringAttribute{Computed: true, Description: "User UUID."},
		"login_name":   schema.StringAttribute{Computed: true, Description: "User login."},
		"name":         schema.StringAttribute{Computed: true, Description: "User display name."},
		"email":        schema.StringAttribute{Computed: true, Description: "User email address."},
		"is_local":     schema.BoolAttribute{Computed: true, Description: "Whether the user authenticates against SonarQube rather than an external provider."},
		"managed":      schema.BoolAttribute{Computed: true, Description: "Whether the user is provisioned by an external system."},
		"scm_accounts": schema.SetAttribute{Computed: true, Description: "SCM accounts (logins or emails) that map commits to this user.", ElementType: types.StringType},
	}
}

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := userAttributes()
	attrs["login_name"] = schema.StringAttribute{Required: true, Description: "Exact login of an active user."}
	resp.Schema = schema.Schema{Description: "Looks up an active user by login.", Attributes: attrs}
}

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var login types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("login_name"), &login)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := findUser(ctx, d.c(), "", login.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read user "+login.ValueString(), err)
		return
	}
	if u == nil {
		resp.Diagnostics.AddError("User not found", fmt.Sprintf("No active user has the login %q.", login.ValueString()))
		return
	}
	m := userData(u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_users

type usersDataSource struct{ dataSourceBase }

func newUsersDataSource() datasource.DataSource { return &usersDataSource{} }

type usersDataModel struct {
	ID     types.String    `tfsdk:"id"`
	Search types.String    `tfsdk:"search"`
	Users  []userDataModel `tfsdk:"users"`
}

func (d *usersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

func (d *usersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists active users, sorted by login.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"search": schema.StringAttribute{Optional: true, Description: "Case-insensitive partial match on login, name and email. Omit to list every user."},
			"users": schema.ListNestedAttribute{Computed: true, Description: "Matching users.",
				NestedObject: schema.NestedAttributeObject{Attributes: userAttributes()}},
		},
	}
}

func (d *usersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m usersDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	users, err := searchUsers(ctx, d.c(), sonarqube.UsersManagementSearchOptions{Q: m.Search.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list users", err)
		return
	}
	slices.SortFunc(users, func(a, b *sonarqube.UserRestResponseForAdmins) int { return cmp.Compare(a.Login, b.Login) })
	m.Users = make([]userDataModel, 0, len(users))
	for _, u := range users {
		m.Users = append(m.Users, userData(u))
	}
	m.ID = types.StringValue("users:" + m.Search.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_user_tokens

type userTokensDataSource struct{ dataSourceBase }

func newUserTokensDataSource() datasource.DataSource { return &userTokensDataSource{} }

type tokenDataModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Type           types.String `tfsdk:"type"`
	CreatedAt      types.String `tfsdk:"created_at"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
	IsExpired      types.Bool   `tfsdk:"is_expired"`
	ProjectKey     types.String `tfsdk:"project_key"`
}

type userTokensDataModel struct {
	ID            types.String     `tfsdk:"id"`
	LoginName     types.String     `tfsdk:"login_name"`
	IgnoreMissing types.Bool       `tfsdk:"ignore_missing"`
	UserTokens    []tokenDataModel `tfsdk:"user_tokens"`
}

func (d *userTokensDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_tokens"
}

func (d *userTokensDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists a user's tokens (names and metadata only; token values are never readable), sorted by name.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Computed: true, Description: "Identifier of this lookup in Terraform state."},
			"login_name":     schema.StringAttribute{Optional: true, Description: "Login of an active user. Omit for the provider's own user."},
			"ignore_missing": schema.BoolAttribute{Optional: true, Description: "Return no tokens, rather than fail, when the user does not exist."},
			"user_tokens": schema.ListNestedAttribute{Computed: true, Description: "The user's tokens.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":              schema.StringAttribute{Computed: true, Description: "`<login>/<name>`, the import ID of `sonarqube_user_token`."},
					"name":            schema.StringAttribute{Computed: true, Description: "Token name."},
					"type":            schema.StringAttribute{Computed: true, Description: "`USER_TOKEN`, `GLOBAL_ANALYSIS_TOKEN` or `PROJECT_ANALYSIS_TOKEN`."},
					"created_at":      schema.StringAttribute{Computed: true, Description: "Creation time as SonarQube reports it (ISO 8601, e.g. `2026-11-15T09:30:00+0000`)."},
					"expiration_date": schema.StringAttribute{Computed: true, Description: "Expiry as `YYYY-MM-DD`, or null for none."},
					"is_expired":      schema.BoolAttribute{Computed: true, Description: "Whether the token has passed its expiration date."},
					"project_key":     schema.StringAttribute{Computed: true, Description: "Project of a project analysis token."},
				}}},
		},
	}
}

func (d *userTokensDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m userTokensDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	login, tokens, err := listTokens(ctx, d.c(), m.LoginName.ValueString())
	m.UserTokens = []tokenDataModel{}
	m.ID = types.StringValue(m.LoginName.ValueString())
	if err != nil {
		if isGone(err) && m.IgnoreMissing.ValueBool() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
		apiError(&resp.Diagnostics, "Unable to list tokens", err)
		return
	}
	m.ID = types.StringValue(login)
	slices.SortFunc(tokens, func(a, b apiToken) int { return cmp.Compare(a.Name, b.Name) })
	for _, t := range tokens {
		tm := tokenDataModel{
			ID: types.StringValue(login + "/" + t.Name), Name: types.StringValue(t.Name), Type: types.StringValue(t.Type),
			CreatedAt: strOrNull(t.CreatedAt), ExpirationDate: types.StringNull(), IsExpired: types.BoolValue(t.IsExpired),
			ProjectKey: types.StringNull(),
		}
		if t.ExpirationDate != "" {
			tm.ExpirationDate = types.StringValue(tokenDate(t.ExpirationDate))
		}
		if t.Project != nil {
			tm.ProjectKey = types.StringValue(t.Project.Key)
		}
		m.UserTokens = append(m.UserTokens, tm)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
