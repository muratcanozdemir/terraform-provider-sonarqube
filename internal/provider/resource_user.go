package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_user

var (
	_ resource.ResourceWithModifyPlan     = (*userResource)(nil)
	_ resource.ResourceWithValidateConfig = (*userResource)(nil)
)

type userResource struct{ resourceBase }

func newUserResource() resource.Resource { return &userResource{} }

type userModel struct {
	ID                types.String `tfsdk:"id"`
	UUID              types.String `tfsdk:"uuid"`
	LoginName         types.String `tfsdk:"login_name"`
	Name              types.String `tfsdk:"name"`
	Email             types.String `tfsdk:"email"`
	Password          types.String `tfsdk:"password"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	PasswordWOVersion types.String `tfsdk:"password_wo_version"`
	IsLocal           types.Bool   `tfsdk:"is_local"`
	SCMAccounts       types.Set    `tfsdk:"scm_accounts"`
}

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A SonarQube user. Destroying it deactivates the user (and anonymizes it when the provider's " +
			"`anonymize_user_on_delete` is set); SonarQube never deletes users.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The login."},
			"uuid": schema.StringAttribute{Computed: true, Description: "User UUID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"login_name": schema.StringAttribute{Required: true,
				Description: "Login. Changing it renames the login in place.",
				Validators:  []validator.String{stringvalidator.LengthBetween(2, 100), noSurroundingSpace{}}},
			"name": schema.StringAttribute{Required: true, Description: "Display name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
			"email": schema.StringAttribute{Optional: true, Description: "Email address.",
				Validators: []validator.String{stringvalidator.LengthAtMost(100)}},
			"password": schema.StringAttribute{Optional: true, Sensitive: true,
				Description: "Password of a local user. Stored in state; prefer `password_wo`.",
				Validators: []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("password_wo")),
					stringvalidator.LengthAtLeast(1)}},
			"password_wo": schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Write-only password of a local user, never stored in state (Terraform 1.11+). " +
					"Change `password_wo_version` to set a new one.",
				Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("password_wo_version"))}},
			"password_wo_version": schema.StringAttribute{Optional: true,
				Description: "Any value; changing it applies the current `password_wo`."},
			"is_local": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the user authenticates with SonarQube (true) or an external provider (false)."},
			"scm_accounts": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "SCM accounts used to attribute issues to the user. When omitted, they are not managed.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *userResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	local := cfg.IsLocal.IsNull() || (!cfg.IsLocal.IsUnknown() && cfg.IsLocal.ValueBool())
	if local && cfg.Password.IsNull() && cfg.PasswordWO.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("password"), "Missing password",
			"Local users (is_local = true, the default) need `password` or `password_wo`.")
	}
	if !local && (!cfg.Password.IsNull() || !cfg.PasswordWO.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("password"), "Password not allowed",
			"Users with is_local = false authenticate externally and cannot have a password.")
	}
}

func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if plan.LoginName.Equal(state.LoginName) {
		resp.Plan.SetAttribute(ctx, path.Root("id"), state.ID)
	}
}

// findUser looks a user up by UUID when known, else by exact login. It
// returns nil for a missing or deactivated user.
func findUser(ctx context.Context, c *sonarqube.Client, uuid, login string) (*sonarqube.UserRestResponseForAdmins, error) {
	if uuid != "" {
		u, _, err := c.UsersManagement.Get(ctx, &sonarqube.UsersManagementGetOptions{ID: uuid})
		if isGone(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !u.Active {
			return nil, nil
		}
		return u, nil
	}
	users, err := collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.UserRestResponseForAdmins, int64, error) {
		res, _, err := c.UsersManagement.Search(ctx, &sonarqube.UsersManagementSearchOptions{Q: login, Active: ptr(true), PageIndex: &page, PageSize: &size})
		if err != nil {
			return nil, 0, err
		}
		var t int64
		if res.Page != nil {
			t = int64(res.Page.Total)
		}
		return res.Users, t, nil
	})
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Login == login {
			return u, nil
		}
	}
	return nil, nil
}

func (r *userResource) apply(m *userModel, u *sonarqube.UserRestResponseForAdmins) {
	m.ID = types.StringValue(u.Login)
	m.UUID = types.StringValue(u.ID)
	m.LoginName = types.StringValue(u.Login)
	m.Name = types.StringValue(u.Name)
	m.Email = keepNullIfEmpty(m.Email, u.Email)
	m.IsLocal = types.BoolValue(u.Local)
	m.SCMAccounts = stringSet(u.SCMAccounts)
	m.PasswordWO = types.StringNull()
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := &sonarqube.UserCreateRestRequest{
		Login: plan.LoginName.ValueString(),
		Name:  plan.Name.ValueString(),
		Local: ptr(plan.IsLocal.ValueBool()),
	}
	if !plan.Email.IsNull() {
		body.Email = ptr(plan.Email.ValueString())
	}
	switch {
	case !cfg.PasswordWO.IsNull():
		body.Password = ptr(cfg.PasswordWO.ValueString()) // write-only: from config, never plan
	case !plan.Password.IsNull():
		body.Password = ptr(plan.Password.ValueString())
	}
	if !plan.SCMAccounts.IsUnknown() {
		body.SCMAccounts = stringsOf(ctx, plan.SCMAccounts, &resp.Diagnostics)
	}
	u, _, err := r.c().UsersManagement.Create(ctx, &sonarqube.UsersManagementCreateOptions{Body: body})
	if err != nil {
		createError(&resp.Diagnostics, "user", plan.LoginName.ValueString(), err)
		return
	}
	r.apply(&plan, u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	login := state.LoginName.ValueString()
	if login == "" {
		login = state.ID.ValueString() // import
	}
	u, err := findUser(ctx, r.c(), state.UUID.ValueString(), login)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read user "+login, err)
		return
	}
	if u == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	r.apply(&state, u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := &sonarqube.UserUpdateRestRequest{}
	changed := false
	if !plan.LoginName.Equal(state.LoginName) {
		body.Login, changed = ptr(plan.LoginName.ValueString()), true
	}
	if !plan.Name.Equal(state.Name) {
		body.Name, changed = ptr(plan.Name.ValueString()), true
	}
	if !plan.Email.Equal(state.Email) {
		body.Email, changed = ptr(plan.Email.ValueString()), true
	}
	if !plan.IsLocal.Equal(state.IsLocal) {
		body.Local, changed = ptr(plan.IsLocal.ValueBool()), true
	}
	lists := map[string][]string{}
	if !plan.SCMAccounts.IsUnknown() && !plan.SCMAccounts.Equal(state.SCMAccounts) {
		lists["scmAccounts"] = stringsOf(ctx, plan.SCMAccounts, &resp.Diagnostics)
		changed = true
	}
	u := (*sonarqube.UserRestResponseForAdmins)(nil)
	if changed {
		u = new(sonarqube.UserRestResponseForAdmins)
		err := mergePatch(ctx, r.c(), "/api/v2/users-management/users/{id}", map[string]string{"id": state.UUID.ValueString()}, body, lists, u)
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to update user "+state.LoginName.ValueString(), err)
			return
		}
	}
	login := plan.LoginName.ValueString()
	newPassword := ""
	switch {
	case !cfg.PasswordWO.IsNull() && !plan.PasswordWOVersion.Equal(state.PasswordWOVersion):
		newPassword = cfg.PasswordWO.ValueString()
	case !plan.Password.IsNull() && !plan.Password.Equal(state.Password):
		newPassword = plan.Password.ValueString()
	}
	if newPassword != "" {
		if _, err := r.c().Users.ChangePassword(ctx, &sonarqube.UsersChangePasswordOptions{Login: login, Password: newPassword}); err != nil {
			apiError(&resp.Diagnostics, "Unable to change password of user "+login, err)
			return
		}
	}
	if u == nil {
		var err error
		if u, err = findUser(ctx, r.c(), state.UUID.ValueString(), login); err != nil || u == nil {
			resp.Diagnostics.AddError("Unable to read user after update", fmt.Sprint(err))
			return
		}
	}
	r.apply(&plan, u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := &sonarqube.UsersManagementDeactivateOptions{ID: state.UUID.ValueString()}
	if r.p.anonymizeUsers {
		opts.Anonymize = ptr(true)
	}
	_, err := r.c().UsersManagement.Deactivate(ctx, opts)
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to deactivate user "+state.LoginName.ValueString(), err)
	}
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("login_name"), req.ID)...)
}

// ---------------------------------------------------------------------------
// sonarqube_user_external_identity

type userExternalIdentityResource struct{ resourceBase }

func newUserExternalIdentityResource() resource.Resource { return &userExternalIdentityResource{} }

type userExternalIdentityModel struct {
	ID               types.String `tfsdk:"id"`
	LoginName        types.String `tfsdk:"login_name"`
	ExternalIdentity types.String `tfsdk:"external_identity"`
	ExternalProvider types.String `tfsdk:"external_provider"`
}

func (r *userExternalIdentityResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_external_identity"
}

func (r *userExternalIdentityResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "The external identity of a non-local user. Destroying it leaves the user's identity unchanged, since SonarQube cannot unset it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"login_name": schema.StringAttribute{Required: true, Description: "User login.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"external_identity": schema.StringAttribute{Required: true, Description: "Identity in the external provider."},
			"external_provider": schema.StringAttribute{Required: true,
				Description: "Provider key, e.g. `LDAP_default`, `github`, `gitlab`, `saml`."},
		},
	}
}

func (r *userExternalIdentityResource) set(ctx context.Context, m *userExternalIdentityModel) error {
	login := m.LoginName.ValueString()
	u, err := findUser(ctx, r.c(), "", login)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("no active user has the login %q", login)
	}
	_, _, err = r.c().UsersManagement.Update(ctx, &sonarqube.UsersManagementUpdateOptions{ID: u.ID,
		Body: &sonarqube.UserUpdateRestRequest{
			ExternalProvider: ptr(m.ExternalProvider.ValueString()),
			ExternalLogin:    ptr(m.ExternalIdentity.ValueString()),
		}})
	return err
}

func (r *userExternalIdentityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userExternalIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		identityError(&resp.Diagnostics, plan.LoginName.ValueString(), err)
		return
	}
	plan.ID = plan.LoginName
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userExternalIdentityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userExternalIdentityModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	login := state.LoginName.ValueString()
	if login == "" {
		login = state.ID.ValueString()
	}
	u, err := findUser(ctx, r.c(), "", login)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read user "+login, err)
		return
	}
	if u == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID = types.StringValue(u.Login)
	state.LoginName = types.StringValue(u.Login)
	state.ExternalIdentity = types.StringValue(u.ExternalLogin)
	state.ExternalProvider = types.StringValue(u.ExternalProvider)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userExternalIdentityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan userExternalIdentityModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		identityError(&resp.Diagnostics, plan.LoginName.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userExternalIdentityResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *userExternalIdentityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("login_name"), req.ID)...)
}

// identityError explains the bare HTTP 500 SonarQube returns for the
// common causes of a rejected identity change.
func identityError(diags *diag.Diagnostics, login string, err error) {
	detail := explain(err)
	if sonarqube.StatusCode(err) == 500 {
		detail += "\n\nSonarQube answers 500 when the external provider is not enabled on the server " +
			"(see api/users/identity_providers), or when another user, including a deactivated one, already has this identity for that provider."
	}
	diags.AddError("Unable to set external identity of "+login, detail)
}

func (r *userResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *userExternalIdentityResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
