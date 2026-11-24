package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_email_configuration

var (
	_ resource.ResourceWithImportState    = (*emailConfigurationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*emailConfigurationResource)(nil)
)

type emailConfigurationResource struct{ resourceBase }

func newEmailConfigurationResource() resource.Resource { return &emailConfigurationResource{} }

type emailConfigurationModel struct {
	ID                      types.String `tfsdk:"id"`
	Host                    types.String `tfsdk:"host"`
	Port                    types.Int64  `tfsdk:"port"`
	SecurityProtocol        types.String `tfsdk:"security_protocol"`
	FromAddress             types.String `tfsdk:"from_address"`
	FromName                types.String `tfsdk:"from_name"`
	SubjectPrefix           types.String `tfsdk:"subject_prefix"`
	AuthMethod              types.String `tfsdk:"auth_method"`
	Username                types.String `tfsdk:"username"`
	BasicPassword           types.String `tfsdk:"basic_password"`
	BasicPasswordWO         types.String `tfsdk:"basic_password_wo"`
	OauthAuthenticationHost types.String `tfsdk:"oauth_authentication_host"`
	OauthTenant             types.String `tfsdk:"oauth_tenant"`
	OauthScope              types.String `tfsdk:"oauth_scope"`
	OauthClientID           types.String `tfsdk:"oauth_client_id"`
	OauthClientIDWO         types.String `tfsdk:"oauth_client_id_wo"`
	OauthClientSecret       types.String `tfsdk:"oauth_client_secret"`
	OauthClientSecretWO     types.String `tfsdk:"oauth_client_secret_wo"`
	CredentialsWOVersion    types.String `tfsdk:"credentials_wo_version"`
}

// The server keeps a single configuration under this ID.
const emailConfigurationID = "email-configuration"

func (r *emailConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_email_configuration"
}

func (r *emailConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	str := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{Required: true, Description: desc, Validators: v}
	}
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Description: desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	}
	secret := func(name, desc string) (schema.StringAttribute, schema.StringAttribute) {
		return schema.StringAttribute{Optional: true, Sensitive: true,
				Description: desc + " Stored in state; prefer `" + name + "_wo`.",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1), stringvalidator.ConflictsWith(path.MatchRoot(name + "_wo"))}},
			schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
				Description: desc + " Write-only, never stored in state (Terraform 1.11+). Change `credentials_wo_version` to send a new value.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1),
					stringvalidator.AlsoRequires(path.MatchRoot("credentials_wo_version"))}}
	}
	pw, pwWO := secret("basic_password", "Password for `BASIC` authentication.")
	id, idWO := secret("oauth_client_id", "Client ID for `OAUTH` authentication.")
	sec, secWO := secret("oauth_client_secret", "Client secret for `OAUTH` authentication.")
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "The SMTP server SonarQube sends notifications through (*Administration > Configuration > Email " +
			"notifications*). The server holds one such configuration; creating this resource fails if one exists " +
			"already: import it instead. SonarQube does not return secrets, so a secret changed outside Terraform is " +
			"only noticed when it is removed. Uses the internal API `api/v2/system/email-configurations` (SonarQube " +
			"Server 10.7 and Community Build 24.12 and later). Import ID: `email-configuration`.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "`email-configuration`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"host": str("SMTP server host.", stringvalidator.LengthAtLeast(1)),
			"port": schema.Int64Attribute{Required: true, Description: "SMTP server port, usually 25, 465 or 587.",
				Validators: []validator.Int64{int64validator.Between(1, 65535)}},
			"security_protocol": str("`NONE`, `SSLTLS` or `STARTTLS`.", stringvalidator.OneOf("NONE", "SSLTLS", "STARTTLS")),
			"from_address":      str("Address emails come from.", stringvalidator.LengthAtLeast(1)),
			"from_name": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("SonarQube"),
				Description: "Name emails come from. Defaults to `SonarQube`."},
			"subject_prefix": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("[SONARQUBE]"),
				Description: "Prefix of email subjects. Defaults to `[SONARQUBE]`."},
			"auth_method": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("BASIC"),
				Description: "`BASIC` (username and password) or `OAUTH` (Microsoft Exchange only). Defaults to `BASIC`.",
				Validators:  []validator.String{stringvalidator.OneOf("BASIC", "OAUTH")}},
			"username":                  str("Username to authenticate to the SMTP server.", stringvalidator.LengthAtLeast(1)),
			"basic_password":            pw,
			"basic_password_wo":         pwWO,
			"oauth_authentication_host": computed("For `OAUTH`: host of the identity provider issuing access tokens."),
			"oauth_tenant":              computed("For `OAUTH`: Microsoft tenant."),
			"oauth_scope":               computed("For `OAUTH`: scope of the token request. The server defaults it to `https://outlook.office365.com/.default`."),
			"oauth_client_id":           id,
			"oauth_client_id_wo":        idWO,
			"oauth_client_secret":       sec,
			"oauth_client_secret_wo":    secWO,
			"credentials_wo_version": schema.StringAttribute{Optional: true,
				Description: "Any value; changing it sends the current `*_wo` credentials."},
		},
	}
}

// ValidateConfig requires the credentials of the chosen method, which the
// server needs on creation.
func (r *emailConfigurationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m emailConfigurationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.AuthMethod.IsUnknown() {
		return
	}
	need := func(attrs ...string) {
		for _, a := range attrs {
			var plain, wo types.String
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a), &plain)...)
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(a+"_wo"), &wo)...)
			if plain.IsNull() && wo.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root(a), "Missing email credentials",
					fmt.Sprintf("auth_method %q needs %s or %s_wo.", authMethod(m.AuthMethod), a, a))
			}
		}
	}
	if authMethod(m.AuthMethod) == "OAUTH" {
		need("oauth_client_id", "oauth_client_secret")
		for _, a := range []struct {
			v    types.String
			name string
		}{{m.OauthAuthenticationHost, "oauth_authentication_host"}, {m.OauthTenant, "oauth_tenant"}} {
			if a.v.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root(a.name), "Missing OAuth setting", "auth_method \"OAUTH\" needs "+a.name+".")
			}
		}
	} else {
		need("basic_password")
	}
}

func authMethod(v types.String) string {
	if v.IsNull() {
		return "BASIC"
	}
	return v.ValueString()
}

// secrets returns the credentials to send: the plain attribute from the plan,
// or else the write-only one from the configuration. On update (state not
// nil) a credential is sent only if it differs from the state or the
// write-only version changed; PATCH leaves the others as they are.
func (m *emailConfigurationModel) secrets(cfg, state *emailConfigurationModel) (pw, id, sec *string) {
	woChanged := state == nil || !m.CredentialsWOVersion.Equal(state.CredentialsWOVersion)
	pick := func(plan, wo, prior types.String) *string {
		switch {
		case !plan.IsNull():
			if state != nil && plan.Equal(prior) && !woChanged {
				return nil
			}
			return new(plan.ValueString())
		case !wo.IsNull() && woChanged:
			return new(wo.ValueString())
		}
		return nil
	}
	var prior emailConfigurationModel
	if state != nil {
		prior = *state
	}
	return pick(m.BasicPassword, cfg.BasicPasswordWO, prior.BasicPassword),
		pick(m.OauthClientID, cfg.OauthClientIDWO, prior.OauthClientID),
		pick(m.OauthClientSecret, cfg.OauthClientSecretWO, prior.OauthClientSecret)
}

// optional returns the value to send for an Optional+Computed attribute:
// nil when unknown (left to the server).
func optional(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return new(v.ValueString())
}

func (r *emailConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg emailConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pw, id, sec := plan.secrets(&cfg, nil)
	res, _, err := r.c().SystemV2.CreateEmailConfiguration(ctx, &sonarqube.SystemV2CreateEmailConfigurationOptions{
		Body: &sonarqube.EmailConfigurationCreateRestRequest{
			Host: plan.Host.ValueString(), Port: strconv.FormatInt(plan.Port.ValueInt64(), 10),
			SecurityProtocol: plan.SecurityProtocol.ValueString(), FromAddress: plan.FromAddress.ValueString(),
			FromName: plan.FromName.ValueString(), SubjectPrefix: plan.SubjectPrefix.ValueString(),
			AuthMethod: plan.AuthMethod.ValueString(), Username: plan.Username.ValueString(), BasicPassword: pw,
			OauthAuthenticationHost: optional(plan.OauthAuthenticationHost), OauthTenant: optional(plan.OauthTenant),
			OauthScope: optional(plan.OauthScope), OauthClientID: id, OauthClientSecret: sec,
		}})
	if err != nil {
		createError(&resp.Diagnostics, "email configuration", emailConfigurationID, err)
		return
	}
	plan.apply(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// apply copies the server's values into m. Secrets are not returned.
func (m *emailConfigurationModel) apply(e *sonarqube.EmailConfigurationResource) {
	m.ID = types.StringValue(e.ID)
	m.Host = types.StringValue(e.Host)
	if p, err := strconv.ParseInt(e.Port, 10, 64); err == nil {
		m.Port = types.Int64Value(p)
	}
	m.SecurityProtocol = types.StringValue(e.SecurityProtocol)
	m.FromAddress = types.StringValue(e.FromAddress)
	m.FromName = types.StringValue(e.FromName)
	m.SubjectPrefix = types.StringValue(e.SubjectPrefix)
	m.AuthMethod = types.StringValue(e.AuthMethod)
	m.Username = types.StringValue(e.Username)
	m.OauthAuthenticationHost = types.StringValue(e.OauthAuthenticationHost)
	m.OauthTenant = types.StringValue(e.OauthTenant)
	// Servers before 2025.x have no scope; keep what was planned.
	if e.OauthScope != "" || m.OauthScope.IsNull() || m.OauthScope.IsUnknown() {
		m.OauthScope = types.StringValue(e.OauthScope)
	}
}

// noticeLostSecrets handles credentials of the active method that the
// server no longer has: a plain one is cleared, and for write-only ones the
// version is cleared, so that the next plan sends them again. Only Read may
// do this; after an apply the state must match the plan.
func (m *emailConfigurationModel) noticeLostSecrets(e *sonarqube.EmailConfigurationResource) {
	type cred struct {
		plain *types.String
		set   bool
	}
	creds := []cred{{&m.BasicPassword, e.IsBasicPasswordSet}}
	if e.AuthMethod == "OAUTH" {
		creds = []cred{{&m.OauthClientID, e.IsOauthClientIDSet}, {&m.OauthClientSecret, e.IsOauthClientSecretSet}}
	}
	for _, c := range creds {
		switch {
		case c.set:
		case !c.plain.IsNull():
			*c.plain = types.StringNull()
		case !m.CredentialsWOVersion.IsNull():
			m.CredentialsWOVersion = types.StringNull()
		}
	}
}

func (r *emailConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state emailConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().SystemV2.GetEmailConfiguration(ctx, &sonarqube.SystemV2GetEmailConfigurationOptions{ID: state.ID.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the email configuration", err)
		return
	}
	state.apply(res)
	state.noticeLostSecrets(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *emailConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg emailConfigurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pw, id, sec := plan.secrets(&cfg, &state)
	// The server refuses a new host without the password, and new OAuth
	// URLs without the client secret, so that a changed destination cannot
	// receive the stored credentials. The configuration has them, write-only
	// ones included.
	configured := func(plain, wo types.String) *string {
		if !plain.IsNull() {
			return new(plain.ValueString())
		}
		return optional(wo)
	}
	if !plan.Host.Equal(state.Host) {
		if pw == nil {
			pw = configured(plan.BasicPassword, cfg.BasicPasswordWO)
		}
		if sec == nil {
			sec = configured(plan.OauthClientSecret, cfg.OauthClientSecretWO)
		}
	}
	if !plan.OauthAuthenticationHost.IsUnknown() && !plan.OauthAuthenticationHost.Equal(state.OauthAuthenticationHost) && sec == nil {
		sec = configured(plan.OauthClientSecret, cfg.OauthClientSecretWO)
	}
	changed := func(p, s types.String) *string {
		if p.Equal(s) || p.IsUnknown() || p.IsNull() {
			return nil
		}
		return new(p.ValueString())
	}
	body := &sonarqube.EmailConfigurationUpdateRestRequest{
		Host: changed(plan.Host, state.Host), SecurityProtocol: changed(plan.SecurityProtocol, state.SecurityProtocol),
		FromAddress: changed(plan.FromAddress, state.FromAddress), FromName: changed(plan.FromName, state.FromName),
		SubjectPrefix: changed(plan.SubjectPrefix, state.SubjectPrefix), AuthMethod: changed(plan.AuthMethod, state.AuthMethod),
		Username: changed(plan.Username, state.Username), BasicPassword: pw,
		OauthAuthenticationHost: changed(plan.OauthAuthenticationHost, state.OauthAuthenticationHost),
		OauthTenant:             changed(plan.OauthTenant, state.OauthTenant), OauthScope: changed(plan.OauthScope, state.OauthScope),
		OauthClientID: id, OauthClientSecret: sec,
	}
	if !plan.Port.Equal(state.Port) {
		body.Port = new(strconv.FormatInt(plan.Port.ValueInt64(), 10))
	}
	res, _, err := r.c().SystemV2.UpdateEmailConfiguration(ctx, &sonarqube.SystemV2UpdateEmailConfigurationOptions{
		ID: state.ID.ValueString(), Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to update the email configuration", err)
		return
	}
	plan.ID = state.ID
	plan.apply(res)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *emailConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state emailConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().SystemV2.DeleteEmailConfiguration(ctx, &sonarqube.SystemV2DeleteEmailConfigurationOptions{ID: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete the email configuration", err)
	}
}

func (r *emailConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *emailConfigurationResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
