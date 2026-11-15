package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	eschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

var tokenTypes = []string{"USER_TOKEN", "GLOBAL_ANALYSIS_TOKEN", "PROJECT_ANALYSIS_TOKEN"}

var dateValidator = stringvalidator.RegexMatches(regexpMust(`^\d{4}-\d{2}-\d{2}$`), "must be a date in YYYY-MM-DD format")

// tokenDate converts the API's "2026-01-02T00:00:00+0000" to "2026-01-02".
func tokenDate(s string) string {
	if t, err := time.Parse("2006-01-02T15:04:05-0700", s); err == nil {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// today returns the current UTC date. Tests replace it to move time forward.
var today = func() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

// tokenExpired reports whether a token with the expiration date expires
// (YYYY-MM-DD) is expired today. SonarQube: a token "past the expiration"
// is no longer usable, so it still works on that date.
func tokenExpired(expires string, now time.Time) bool {
	exp, err := time.Parse("2006-01-02", expires)
	return err == nil && now.After(exp)
}

// rotationDue reports whether a token expiring on expires (YYYY-MM-DD) must
// be replaced today, rotateBefore days ahead of its expiration date.
func rotationDue(expires string, rotateBefore int64, now time.Time) bool {
	exp, err := time.Parse("2006-01-02", expires)
	if err != nil {
		return false
	}
	return !now.Before(exp.AddDate(0, 0, -int(rotateBefore)))
}

// ---------------------------------------------------------------------------
// sonarqube_user_token

var (
	_ resource.ResourceWithModifyPlan     = (*userTokenResource)(nil)
	_ resource.ResourceWithValidateConfig = (*userTokenResource)(nil)
)

type userTokenResource struct{ resourceBase }

func newUserTokenResource() resource.Resource { return &userTokenResource{} }

type userTokenModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	LoginName      types.String `tfsdk:"login_name"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
	ExpirationDays types.Int64  `tfsdk:"expiration_days"`
	RotateBefore   types.Int64  `tfsdk:"rotate_before_days"`
	MetadataOnly   types.Bool   `tfsdk:"metadata_only"`
	Token          types.String `tfsdk:"token"`
	Type           types.String `tfsdk:"type"`
	ProjectKey     types.String `tfsdk:"project_key"`
}

func (r *userTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_token"
}

func (r *userTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A user token. The token value is stored in state; to use a token only during a run without storing it, " +
			"use the `sonarqube_user_token` ephemeral resource. With `expiration_days` and `rotate_before_days`, the token is " +
			"replaced by a new one when it nears expiry; a plan made inside the rotation window shows the replacement. " +
			"Import ID: `login/name`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace,
				Description: "Token name, unique per user. Changing it creates a new token.",
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 100)}},
			"login_name": schema.StringAttribute{Optional: true, Computed: true,
				Description:   "User owning the token. Defaults to the authenticated user.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}},
			"expiration_date": schema.StringAttribute{Optional: true, Computed: true,
				Description:   "Expiration date (YYYY-MM-DD). Defaults to no expiration, or the server's maximum lifetime. Conflicts with `expiration_days`.",
				Validators:    []validator.String{dateValidator, stringvalidator.ConflictsWith(path.MatchRoot("expiration_days"))},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}},
			"expiration_days": schema.Int64Attribute{Optional: true,
				Description:   "Lifetime of the token in days, counted from the day it is issued; `expiration_date` then shows the resulting date. Changing it issues a new token.",
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"rotate_before_days": schema.Int64Attribute{Optional: true,
				Description: "Replace the token this many days before it expires (`0`: on its expiration date, its last usable day). Requires `expiration_days`, and must be less than it. " +
					"The replacement happens at the first apply in that window, so run Terraform regularly enough to catch it.",
				Validators: []validator.Int64{int64validator.AtLeast(0), int64validator.AlsoRequires(path.MatchRoot("expiration_days"))}},
			"metadata_only": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Adopt an already issued token: track its metadata and revoke it on destroy, but never store its value. " +
					"Creation fails if the named token does not exist or does not match `type`, `project_key` and `expiration_date`."},
			"token": schema.StringAttribute{Computed: true, Sensitive: true, Description: "The token value."},
			"type": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("USER_TOKEN"),
				PlanModifiers: replace, Validators: []validator.String{stringvalidator.OneOf(tokenTypes...)},
				Description: "One of " + strings.Join(tokenTypes, ", ") + "."},
			"project_key": schema.StringAttribute{Optional: true, PlanModifiers: replace,
				Description: "Project of a PROJECT_ANALYSIS_TOKEN."},
		},
	}
}

func (r *userTokenResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg userTokenModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if cfg.Type.IsUnknown() || cfg.ProjectKey.IsUnknown() {
		return
	}
	if !cfg.RotateBefore.IsNull() && !cfg.RotateBefore.IsUnknown() && !cfg.ExpirationDays.IsNull() && !cfg.ExpirationDays.IsUnknown() &&
		cfg.RotateBefore.ValueInt64() >= cfg.ExpirationDays.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("rotate_before_days"), "Invalid rotate_before_days",
			"rotate_before_days must be less than expiration_days; otherwise every new token would be due for rotation at once.")
	}
	if cfg.MetadataOnly.ValueBool() && (!cfg.ExpirationDays.IsNull() || !cfg.RotateBefore.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("metadata_only"), "Cannot rotate an adopted token",
			"expiration_days and rotate_before_days issue new tokens, which an adopted (metadata_only) token does not allow.")
	}
	isProject := cfg.Type.ValueString() == "PROJECT_ANALYSIS_TOKEN"
	if isProject && cfg.ProjectKey.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("project_key"), "Missing project_key", "PROJECT_ANALYSIS_TOKEN needs project_key.")
	}
	if !isProject && !cfg.ProjectKey.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("project_key"), "Unexpected project_key", "project_key is only valid with type PROJECT_ANALYSIS_TOKEN.")
	}
}

// ModifyPlan refuses to replace an adopted token: the provider cannot
// reissue a credential it never held, so replacement would only revoke it.
func (r *userTokenResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan userTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.MetadataOnly.ValueBool() || plan.MetadataOnly.ValueBool() {
		for _, f := range []struct {
			name      string
			old, next types.String
		}{
			{"name", state.Name, plan.Name}, {"type", state.Type, plan.Type}, {"project_key", state.ProjectKey, plan.ProjectKey},
			{"expiration_date", state.ExpirationDate, plan.ExpirationDate}, {"login_name", state.LoginName, plan.LoginName},
		} {
			if !f.next.IsUnknown() && !f.old.Equal(f.next) {
				resp.Diagnostics.AddAttributeError(path.Root(f.name), "Cannot replace an adopted token",
					fmt.Sprintf("Changing %s would revoke a token this provider cannot reissue (metadata_only). Issue a new token, adopt it at a new resource address, then destroy this one.", f.name))
			}
		}
	}
	if !plan.MetadataOnly.Equal(state.MetadataOnly) {
		// Dependents must stop using the stored value in this same apply.
		resp.Plan.SetAttribute(ctx, path.Root("token"), types.StringUnknown())
	}
	r.planRotation(ctx, &state, &plan, resp)
}

// planRotation replaces a renewable token that is due for rotation, and
// warns about an expired token that cannot be renewed as configured.
func (r *userTokenResource) planRotation(ctx context.Context, state, plan *userTokenModel, resp *resource.ModifyPlanResponse) {
	expires := state.ExpirationDate.ValueString()
	if expires == "" {
		return
	}
	renewable := !plan.ExpirationDays.IsNull() && !plan.ExpirationDays.IsUnknown() && !plan.MetadataOnly.ValueBool()
	rotateBefore := plan.RotateBefore.ValueInt64() // 0 when null: on the expiration date
	if renewable && rotationDue(expires, rotateBefore, today()) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("expiration_date"))
		for _, a := range []string{"id", "token", "expiration_date"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), types.StringUnknown())...)
		}
		return
	}
	if !renewable && tokenExpired(expires, today()) {
		resp.Diagnostics.AddAttributeWarning(path.Root("expiration_date"), "Token expired",
			fmt.Sprintf("Token %q expired after %s. Set expiration_days (and rotate_before_days) to have Terraform issue a new one, or replace it with -replace.", state.Name.ValueString(), expires))
	}
}

type apiToken struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	ExpirationDate string `json:"expirationDate"`
	IsExpired      bool   `json:"isExpired"`
	CreatedAt      string `json:"createdAt"`
	Project        *struct {
		Key string `json:"key"`
	} `json:"project"`
}

func listTokens(ctx context.Context, c *sonarqube.Client, login string) (string, []apiToken, error) {
	_, raw, err := c.UserTokens.Search(ctx, &sonarqube.UserTokensSearchOptions{Login: login})
	if err != nil {
		return "", nil, err
	}
	var out struct {
		Login  string     `json:"login"`
		Tokens []apiToken `json:"userTokens"`
	}
	if err := json.Unmarshal(raw.Body, &out); err != nil {
		return "", nil, err
	}
	return out.Login, out.Tokens, nil
}

func (r *userTokenResource) matches(m *userTokenModel, t apiToken) error {
	if t.Type != m.Type.ValueString() {
		return fmt.Errorf("token %q has type %s, expected %s", t.Name, t.Type, m.Type.ValueString())
	}
	if t.Type == "PROJECT_ANALYSIS_TOKEN" && (t.Project == nil || t.Project.Key != m.ProjectKey.ValueString()) {
		return fmt.Errorf("token %q is not for project %s", t.Name, m.ProjectKey.ValueString())
	}
	if !m.ExpirationDate.IsNull() && !m.ExpirationDate.IsUnknown() && tokenDate(t.ExpirationDate) != m.ExpirationDate.ValueString() {
		return fmt.Errorf("token %q expires %s, expected %s", t.Name, tokenDate(t.ExpirationDate), m.ExpirationDate.ValueString())
	}
	return nil
}

func (r *userTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	if plan.MetadataOnly.ValueBool() {
		login, tokens, err := listTokens(ctx, r.c(), plan.LoginName.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to list tokens", err)
			return
		}
		for _, t := range tokens {
			if t.Name != name {
				continue
			}
			if err := r.matches(&plan, t); err != nil {
				resp.Diagnostics.AddError("Token does not match", err.Error())
				return
			}
			r.applyToken(&plan, login, t)
			plan.Token = types.StringNull()
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			return
		}
		resp.Diagnostics.AddError("Token not found", fmt.Sprintf("metadata_only adopts an existing token, but %q has no token named %q.", login, name))
		return
	}
	expires := plan.ExpirationDate.ValueString()
	if !plan.ExpirationDays.IsNull() {
		expires = today().AddDate(0, 0, int(plan.ExpirationDays.ValueInt64())).Format("2006-01-02")
	}
	res, _, err := r.c().UserTokens.Generate(ctx, &sonarqube.UserTokensGenerateOptions{
		Name:           name,
		Login:          plan.LoginName.ValueString(),
		Type:           plan.Type.ValueString(),
		ProjectKey:     plan.ProjectKey.ValueString(),
		ExpirationDate: expires,
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to generate token "+name, err)
		return
	}
	plan.ID = types.StringValue(res.Login + "/" + res.Name)
	plan.LoginName = types.StringValue(res.Login)
	plan.Token = types.StringValue(res.Token)
	plan.ExpirationDate = strOrNull(tokenDate(res.ExpirationDate))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userTokenResource) applyToken(m *userTokenModel, login string, t apiToken) {
	m.ID = types.StringValue(login + "/" + t.Name)
	m.Name = types.StringValue(t.Name)
	m.LoginName = types.StringValue(login)
	m.Type = types.StringValue(t.Type)
	m.ExpirationDate = strOrNull(tokenDate(t.ExpirationDate))
	if t.Project != nil {
		m.ProjectKey = types.StringValue(t.Project.Key)
	}
}

func (r *userTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	parts, err := splitID(state.ID.ValueString(), "/", 2, "login/name")
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	login, tokens, err := listTokens(ctx, r.c(), parts[0])
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list tokens of "+parts[0], err)
		return
	}
	for _, t := range tokens {
		if t.Name == parts[1] {
			r.applyToken(&state, login, t)
			if state.MetadataOnly.IsNull() {
				state.MetadataOnly = types.BoolValue(false)
			}
			if state.MetadataOnly.ValueBool() {
				state.Token = types.StringNull()
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // revoked outside Terraform
}

// Update only toggles metadata_only; everything else forces replacement.
func (r *userTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	plan.Token = types.StringNull() // a value dropped from state cannot be recovered
	if !plan.MetadataOnly.ValueBool() && !state.MetadataOnly.ValueBool() {
		plan.Token = state.Token
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().UserTokens.Revoke(ctx, &sonarqube.UserTokensRevokeOptions{Login: state.LoginName.ValueString(), Name: state.Name.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to revoke token "+state.Name.ValueString(), err)
	}
}

func (r *userTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := splitID(req.ID, "/", 2, "login/name"); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	// The value of an existing token cannot be read; imported tokens are
	// therefore tracked as metadata only.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("metadata_only"), true)...)
}

// ---------------------------------------------------------------------------
// ephemeral sonarqube_user_token

var _ ephemeral.EphemeralResourceWithClose = (*userTokenEphemeral)(nil)

type userTokenEphemeral struct{ ephemeralBase }

func newUserTokenEphemeralResource() ephemeral.EphemeralResource { return &userTokenEphemeral{} }

type userTokenEphemeralModel struct {
	NamePrefix     types.String `tfsdk:"name_prefix"`
	Name           types.String `tfsdk:"name"`
	LoginName      types.String `tfsdk:"login_name"`
	Type           types.String `tfsdk:"type"`
	ProjectKey     types.String `tfsdk:"project_key"`
	ExpirationDate types.String `tfsdk:"expiration_date"`
	Token          types.String `tfsdk:"token"`
}

func (e *userTokenEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_token"
}

func (e *userTokenEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = eschema.Schema{
		Description: "A short-lived token, generated when Terraform opens it and revoked when the run ends. It is never stored in state or plan files.",
		Attributes: map[string]eschema.Attribute{
			"name_prefix":     eschema.StringAttribute{Optional: true, Description: "Prefix of the generated name. Defaults to `terraform-`."},
			"name":            eschema.StringAttribute{Computed: true, Description: "Generated token name."},
			"login_name":      eschema.StringAttribute{Optional: true, Computed: true, Description: "User owning the token. Defaults to the authenticated user."},
			"type":            eschema.StringAttribute{Optional: true, Computed: true, Description: "Token type, as for the resource. Defaults to USER_TOKEN.", Validators: []validator.String{stringvalidator.OneOf(tokenTypes...)}},
			"project_key":     eschema.StringAttribute{Optional: true, Description: "Project of a PROJECT_ANALYSIS_TOKEN."},
			"expiration_date": eschema.StringAttribute{Optional: true, Computed: true, Description: "Expiration date (YYYY-MM-DD). Defaults to two days ahead, as a safety net should revocation never happen.", Validators: []validator.String{dateValidator}},
			"token":           eschema.StringAttribute{Computed: true, Sensitive: true, Description: "The token value."},
		},
	}
}

func (e *userTokenEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	if e.p == nil {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	var m userTokenEphemeralModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	prefix := m.NamePrefix.ValueString()
	if m.NamePrefix.IsNull() {
		prefix = "terraform-"
	}
	typ := m.Type.ValueString()
	if typ == "" {
		typ = "USER_TOKEN"
	}
	expires := m.ExpirationDate.ValueString()
	if expires == "" {
		// A safety net: should Terraform never call Close (a crash, or a
		// failure after Open), the token still stops working.
		expires = time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	}
	res, _, err := e.p.client.UserTokens.Generate(ctx, &sonarqube.UserTokensGenerateOptions{
		Name:           prefix + randSuffix(),
		Login:          m.LoginName.ValueString(),
		Type:           typ,
		ProjectKey:     m.ProjectKey.ValueString(),
		ExpirationDate: expires,
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to generate token", err)
		return
	}
	m.Name = types.StringValue(res.Name)
	m.LoginName = types.StringValue(res.Login)
	m.Type = types.StringValue(typ)
	m.ExpirationDate = types.StringValue(tokenDate(res.ExpirationDate))
	m.Token = types.StringValue(res.Token)
	b, _ := json.Marshal(map[string]string{"login": res.Login, "name": res.Name})
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, "token", b)...)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		// Terraform does not call Close after a failed Open: revoke now.
		_, _ = e.p.client.UserTokens.Revoke(ctx, &sonarqube.UserTokensRevokeOptions{Login: res.Login, Name: res.Name})
	}
}

func (e *userTokenEphemeral) Close(ctx context.Context, req ephemeral.CloseRequest, resp *ephemeral.CloseResponse) {
	if e.p == nil {
		return // never opened
	}
	b, d := req.Private.GetKey(ctx, "token")
	resp.Diagnostics.Append(d...)
	var t struct{ Login, Name string }
	if err := json.Unmarshal(b, &t); err != nil || t.Name == "" {
		return
	}
	_, err := e.p.client.UserTokens.Revoke(ctx, &sonarqube.UserTokensRevokeOptions{Login: t.Login, Name: t.Name})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to revoke ephemeral token "+t.Name, err)
	}
}

func (r *userTokenResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
