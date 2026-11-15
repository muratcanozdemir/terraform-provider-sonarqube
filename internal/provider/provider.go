// Package provider implements the SonarQube Terraform provider on top of
// sonarqube-go-client.
package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

var (
	_ provider.Provider                       = (*sonarqubeProvider)(nil)
	_ provider.ProviderWithEphemeralResources = (*sonarqubeProvider)(nil)
)

// New returns a provider factory for the given provider version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &sonarqubeProvider{version: version} }
}

type sonarqubeProvider struct {
	version string
}

type providerModel struct {
	Host                  types.String  `tfsdk:"host"`
	Token                 types.String  `tfsdk:"token"`
	User                  types.String  `tfsdk:"user"`
	Pass                  types.String  `tfsdk:"pass"`
	HTTPProxy             types.String  `tfsdk:"http_proxy"`
	InstalledVersion      types.String  `tfsdk:"installed_version"`
	InstalledEdition      types.String  `tfsdk:"installed_edition"`
	TLSInsecureSkipVerify types.Bool    `tfsdk:"tls_insecure_skip_verify"`
	TLSCACertificate      types.String  `tfsdk:"tls_ca_certificate"`
	AnonymizeUserOnDelete types.Bool    `tfsdk:"anonymize_user_on_delete"`
	RequestTimeout        types.String  `tfsdk:"request_timeout"`
	MaxRequestsPerSecond  types.Float64 `tfsdk:"max_requests_per_second"`
	MaxRetries            types.Int64   `tfsdk:"max_retries"`
}

func (p *sonarqubeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "sonarqube"
	resp.Version = p.version
}

func (p *sonarqubeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages SonarQube Server and Community Build through the Web API.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "SonarQube base URL, e.g. `https://sonar.example.com` or `http://localhost:9000/sonar`. Falls back to `SONAR_HOST`, `SONARQUBE_HOST` or `SONAR_HOST_URL`.",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "User token. Falls back to `SONAR_TOKEN` or `SONARQUBE_TOKEN`. Conflicts with `user`/`pass`.",
			},
			"user": schema.StringAttribute{
				Optional:    true,
				Description: "Login for basic authentication. Falls back to `SONAR_USER` or `SONARQUBE_USER`. Requires `pass`.",
			},
			"pass": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Password for basic authentication. Falls back to `SONAR_PASS` or `SONARQUBE_PASS`. Requires `user`.",
			},
			"http_proxy": schema.StringAttribute{
				Optional:    true,
				Description: "Proxy URL. When unset, the standard `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` variables apply.",
			},
			"installed_version": schema.StringAttribute{
				Optional:    true,
				Description: "Server version, e.g. `2026.1` or `26.11`. Skips version detection. Falls back to `INSTALLED_VERSION`.",
				Validators:  []validator.String{versionStringValidator{}},
			},
			"installed_edition": schema.StringAttribute{
				Optional:    true,
				Description: "Server edition (`community`, `developer`, `enterprise`, `datacenter`). Skips edition detection. Falls back to `INSTALLED_EDITION`.",
				Validators: []validator.String{stringvalidator.OneOfCaseInsensitive(
					"community", "developer", "enterprise", "datacenter")},
			},
			"tls_insecure_skip_verify": schema.BoolAttribute{
				Optional:    true,
				Description: "Skip TLS certificate verification. Only for local testing.",
			},
			"tls_ca_certificate": schema.StringAttribute{
				Optional:    true,
				Description: "PEM-encoded CA certificate(s) to trust in addition to the system pool, for servers behind a private CA.",
			},
			"anonymize_user_on_delete": schema.BoolAttribute{
				Optional:    true,
				Description: "Anonymize users when they are destroyed, in addition to deactivating them. Defaults to `false`.",
			},
			"request_timeout": schema.StringAttribute{
				Optional:    true,
				Description: "Timeout of a single HTTP request, as a Go duration. Defaults to `60s`.",
				Validators:  []validator.String{durationValidator{}},
			},
			"max_requests_per_second": schema.Float64Attribute{
				Optional:    true,
				Description: "Client-side rate limit shared by all operations of this provider instance. Defaults to `20`. `0` disables it.",
				Validators:  []validator.Float64{float64validator.AtLeast(0)},
			},
			"max_retries": schema.Int64Attribute{
				Optional: true,
				Description: "Retries for throttled (429) requests, and for idempotent requests that fail with 502/503/504 " +
					"or a connection error. Non-idempotent requests are never replayed after the server may have processed them. Defaults to `3`.",
				Validators: []validator.Int64{int64validator.Between(0, 10)},
			},
		},
	}
}

// envOr returns the configured value, or the first non-empty environment variable.
func envOr(v types.String, keys ...string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	for _, k := range keys {
		if s := os.Getenv(k); s != "" {
			return s
		}
	}
	return ""
}

func (p *sonarqubeProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Values that depend on other resources are unknown during plan; the
	// provider is configured again with known values before apply.
	for _, v := range []types.String{cfg.Host, cfg.Token, cfg.User, cfg.Pass} {
		if v.IsUnknown() {
			if req.ClientCapabilities.DeferralAllowed {
				// Terraform defers every resource of this provider to a later round.
				resp.Deferred = &provider.Deferred{Reason: provider.DeferredReasonProviderConfigUnknown}
				return
			}
			resp.Diagnostics.AddWarning("Provider configuration not yet known",
				"The SonarQube provider configuration depends on values known only after apply. Resources that need the API cannot be planned accurately until then.")
			return
		}
	}

	host := envOr(cfg.Host, "SONAR_HOST", "SONARQUBE_HOST", "SONAR_HOST_URL")
	token := envOr(cfg.Token, "SONAR_TOKEN", "SONARQUBE_TOKEN")
	user := envOr(cfg.User, "SONAR_USER", "SONARQUBE_USER")
	pass := envOr(cfg.Pass, "SONAR_PASS", "SONARQUBE_PASS")

	if host == "" {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Missing SonarQube host",
			"Set `host` in the provider block or the SONAR_HOST environment variable.")
	}
	switch {
	case token != "" && (user != "" || pass != ""):
		// A token in the environment plus explicit basic auth in the
		// configuration (or vice versa) is ambiguous; refuse to guess.
		resp.Diagnostics.AddError("Conflicting SonarQube credentials",
			"Both a token and user/pass are set (in the provider block or the environment). Configure exactly one.")
	case token == "" && (user == "") != (pass == ""):
		resp.Diagnostics.AddError("Incomplete SonarQube credentials", "`user` and `pass` must be set together.")
	case token == "" && user == "":
		resp.Diagnostics.AddError("Missing SonarQube credentials",
			"Set `token` (or SONAR_TOKEN), or `user` and `pass` (or SONAR_USER and SONAR_PASS).")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	timeout := 60 * time.Second
	if !cfg.RequestTimeout.IsNull() {
		timeout, _ = time.ParseDuration(cfg.RequestTimeout.ValueString()) // validated
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if proxy := cfg.HTTPProxy.ValueString(); proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("http_proxy"), "Invalid proxy URL", err.Error())
			return
		}
		transport.Proxy = http.ProxyURL(u)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLSInsecureSkipVerify.ValueBool() {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 -- explicit opt-in for local testing
	}
	if pem := cfg.TLSCACertificate.ValueString(); pem != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			resp.Diagnostics.AddAttributeError(path.Root("tls_ca_certificate"), "Invalid CA certificate",
				"No PEM certificate could be parsed from tls_ca_certificate.")
			return
		}
		tlsCfg.RootCAs = pool
	}
	transport.TLSClientConfig = tlsCfg

	rps := 20.0
	if !cfg.MaxRequestsPerSecond.IsNull() {
		rps = cfg.MaxRequestsPerSecond.ValueFloat64()
	}
	burst := max(int(rps), 1)
	retry := sonarqube.DefaultRetryPolicy
	if !cfg.MaxRetries.IsNull() {
		retry.MaxRetries = int(cfg.MaxRetries.ValueInt64())
	}

	opts := []sonarqube.Option{
		sonarqube.WithHTTPClient(&http.Client{Transport: transport, Timeout: timeout}),
		sonarqube.WithUserAgent(fmt.Sprintf("terraform-provider-sonarqube/%s (+%s)", p.version, "sonarqube-go-client/"+sonarqube.Version)),
		sonarqube.WithRateLimit(rps, burst),
		sonarqube.WithRetryPolicy(retry),
	}
	if token != "" {
		opts = append(opts, sonarqube.WithToken(token))
	} else {
		opts = append(opts, sonarqube.WithBasicAuth(user, pass))
	}
	client, err := sonarqube.NewClient(host, opts...)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Invalid SonarQube host", err.Error())
		return
	}

	data := &providerData{
		client:         client,
		anonymizeUsers: cfg.AnonymizeUserOnDelete.ValueBool(),
		locks:          newKeyedMutex(),
		info: &serverInfoCache{
			client:          client,
			versionOverride: envOr(cfg.InstalledVersion, "INSTALLED_VERSION"),
			editionOverride: strings.ToLower(envOr(cfg.InstalledEdition, "INSTALLED_EDITION")),
		},
	}
	tflog.Debug(ctx, "configured SonarQube client", map[string]any{"host": client.BaseURL(), "rps": rps, "max_retries": retry.MaxRetries})
	resp.DataSourceData = data
	resp.ResourceData = data
	resp.EphemeralResourceData = data
	resp.ListResourceData = data
	resp.ActionData = data
}

func (p *sonarqubeProvider) Resources(context.Context) []func() resource.Resource {
	return guardAll([]func() resource.Resource{
		withIdentity(newAlmAzureResource, keyIdentity("DevOps platform setting")),
		withIdentity(newAlmBitbucketResource, keyIdentity("DevOps platform setting")),
		withIdentity(newAlmBitbucketCloudResource, keyIdentity("DevOps platform setting")),
		withIdentity(newAlmGithubResource, keyIdentity("DevOps platform setting")),
		withIdentity(newAlmGitlabResource, keyIdentity("DevOps platform setting")),
		withIdentity(newApplicationResource, fixedKeyIdentity("Application")),
		withIdentity(newApplicationBranchResource, appBranchIdentity),
		newAzureBindingResource,
		newBitbucketBindingResource,
		newBitbucketCloudBindingResource,
		newGithubBindingResource,
		newGitlabBindingResource,
		withIdentity(newGroupResource, nameIdentity("Group")),
		newGroupMemberResource,
		newNewCodePeriodsResource,
		withIdentity(newPermissionTemplateResource, nameIdentity("Permission template")),
		newPermissionTemplateDefaultResource,
		newPermissionsResource,
		newPluginResource,
		withIdentity(newPortfolioResource, fixedKeyIdentity("Portfolio")),
		withIdentity(newProjectResource, projectIdentity),
		newProjectLinkResource,
		newProjectMainBranchResource,
		newBranchRetentionResource,
		newGithubConfigResource,
		newGitlabConfigResource,
		newScimResource,
		withIdentity(newQualityGateResource, nameIdentity("Quality gate")),
		newQualityGateProjectAssociationResource,
		newQualityGateUsergroupAssociationResource,
		withIdentity(newQualityProfileResource, profileIdentity),
		newQualityProfileActivateRuleResource,
		newQualityProfileDeactivateRuleResource,
		newQualityProfileProjectAssociationResource,
		newQualityProfileUsergroupAssociationResource,
		newRuleResource,
		newSecuredSettingResource,
		newSettingResource,
		withIdentity(newUserResource, userIdentity),
		newUserExternalIdentityResource,
		newUserTokenResource,
		newWebhookResource,
		withIdentity(newEmailConfigurationResource, emailIdentity),
	}, guardResource)
}

// guardResource guards a resource not already guarded by withIdentity.
func guardResource(newResource func() resource.Resource) func() resource.Resource {
	if _, ok := newResource().(*identityResource); ok {
		return newResource
	}
	return guarded(newResource)
}

func (p *sonarqubeProvider) DataSources(context.Context) []func() datasource.DataSource {
	return guardAll([]func() datasource.DataSource{
		newAlmAzureDataSource,
		newAlmBitbucketDataSource,
		newAlmBitbucketCloudDataSource,
		newAlmGithubDataSource,
		newAlmGitlabDataSource,
		newGroupDataSource,
		newGroupMembersDataSource,
		newGroupsDataSource,
		newLanguagesDataSource,
		newMeasuresDataSource,
		newPermissionTemplatesDataSource,
		newPortfolioDataSource,
		newProjectDataSource,
		newProjectBranchesDataSource,
		newQualityGateDataSource,
		newQualityGatesDataSource,
		newQualityGateStatusDataSource,
		newQualityProfileDataSource,
		newQualityProfileActiveRulesDataSource,
		newQualityProfileDeactivatedRulesDataSource,
		newQualityProfilesDataSource,
		newRuleDataSource,
		newRulesDataSource,
		newServerDataSource,
		newUserDataSource,
		newUserTokensDataSource,
		newUsersDataSource,
	}, guardedDataSource)
}

func (p *sonarqubeProvider) EphemeralResources(context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		newUserTokenEphemeralResource,
	}
}
