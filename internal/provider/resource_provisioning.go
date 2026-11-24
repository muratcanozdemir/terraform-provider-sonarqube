package provider

import (
	"context"
	"fmt"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// GitHub and GitLab authentication and provisioning. Each is a single
// configuration per instance, stored by SonarQube in the sonar.auth.github.*
// and sonar.auth.gitlab.* settings: do not also manage those keys with
// sonarqube_setting or sonarqube_secured_setting.

var provisioningTypes = []string{"JIT", "AUTO_PROVISIONING"}

// secretAttrs returns a sensitive attribute stored in state and its
// write-only form, applied when credentials_wo_version changes. Exactly one
// of them is required when required is set.
func secretAttrs(name, desc string, required bool) map[string]schema.Attribute {
	wo := name + "_wo"
	both := []path.Expression{path.MatchRoot(name), path.MatchRoot(wo)}
	pick := []validator.String{stringvalidator.ConflictsWith(path.MatchRoot(wo))}
	woValidators := []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("credentials_wo_version"))}
	if required {
		pick = []validator.String{stringvalidator.ExactlyOneOf(both...)}
		woValidators = append(woValidators, stringvalidator.ExactlyOneOf(both...))
	}
	return map[string]schema.Attribute{
		name: schema.StringAttribute{Optional: true, Sensitive: true, Validators: pick,
			Description: desc + " Stored in state; prefer `" + wo + "`. SonarQube does not return it, so drift cannot be detected."},
		wo: schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, Validators: woValidators,
			Description: "Write-only form of `" + name + "`, never stored in state (Terraform 1.11+). Change `credentials_wo_version` to apply a new value."},
	}
}

// secret returns the value to send: the stored attribute, else the
// write-only one from the configuration, else nil.
func secret(plan, cfg types.String) *string {
	switch {
	case !plan.IsNull() && !plan.IsUnknown():
		return new(plan.ValueString())
	case !cfg.IsNull() && !cfg.IsUnknown():
		return new(cfg.ValueString())
	}
	return nil
}

func setOfStrings(vs []string) types.Set {
	elems := make([]attr.Value, len(vs))
	for i, v := range vs {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func emptyStringSet() types.Set { return types.SetValueMust(types.StringType, []attr.Value{}) }

func commonProvisioningAttrs(platform, members string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, Description: "Configuration ID.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enabled": schema.BoolAttribute{Required: true, Description: "Whether users can log in with " + platform + "."},
		"provisioning_type": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("JIT"),
			Validators: []validator.String{stringvalidator.OneOf(provisioningTypes...)},
			Description: "`JIT` creates users at their first login. `AUTO_PROVISIONING` synchronizes " + members + " with SonarQube " +
				"hourly; it needs Developer edition or above."},
		"synchronize_groups": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			Description: "With `JIT`, add users at login to the SonarQube groups named like their " + platform + " groups or teams."},
		"allow_users_to_sign_up": schema.BoolAttribute{Optional: true, Computed: true,
			Description:   "With `JIT`, whether users who do not exist in SonarQube yet can sign up. Defaults to the server's choice.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"credentials_wo_version": schema.StringAttribute{Optional: true,
			Description: "Any value; changing it applies the current values of the `_wo` arguments."},
		"error_message": schema.StringAttribute{Computed: true,
			Description: "Why SonarQube considers the configuration invalid, if it does."},
	}
}

func merge(ms ...map[string]schema.Attribute) map[string]schema.Attribute {
	out := map[string]schema.Attribute{}
	for _, m := range ms {
		maps.Copy(out, m)
	}
	return out
}

// requireAutoProvisioningEdition rejects AUTO_PROVISIONING below Developer
// edition at plan time.
func requireAutoProvisioningEdition(ctx context.Context, r *resourceBase, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !r.configured() {
		return
	}
	var pt types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("provisioning_type"), &pt)...)
	if pt.ValueString() == "AUTO_PROVISIONING" {
		r.p.requireEdition(ctx, &resp.Diagnostics, "AUTO_PROVISIONING", "developer")
	}
}

// singletonExists adds an error suggesting an import when the instance
// already has a configuration, which SonarQube allows only one of.
func singletonExists(diags *resource.CreateResponse, what, id string) {
	diags.Diagnostics.AddError("Unable to create "+what,
		fmt.Sprintf("SonarQube already has a %[1]s (ID %[2]q), and allows only one. To manage it with Terraform, import it:\n\n"+
			"  terraform import <address> %[2]q\n\nor add an `import` block with id = %[2]q.", what, id))
}

// ---------------------------------------------------------------------------
// sonarqube_github_configuration

var (
	_ resource.ResourceWithModifyPlan = (*githubConfigResource)(nil)
	_ resource.ResourceWithModifyPlan = (*gitlabConfigResource)(nil)
)

type githubConfigResource struct{ resourceBase }

func (r *githubConfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	requireAutoProvisioningEdition(ctx, &r.resourceBase, req, resp)
}

func newGithubConfigResource() resource.Resource { return &githubConfigResource{} }

type githubConfigModel struct {
	ID                   types.String `tfsdk:"id"`
	Enabled              types.Bool   `tfsdk:"enabled"`
	ClientID             types.String `tfsdk:"client_id"`
	ClientSecret         types.String `tfsdk:"client_secret"`
	ClientSecretWO       types.String `tfsdk:"client_secret_wo"`
	ApplicationID        types.String `tfsdk:"application_id"`
	PrivateKey           types.String `tfsdk:"private_key"`
	PrivateKeyWO         types.String `tfsdk:"private_key_wo"`
	CredentialsWOVersion types.String `tfsdk:"credentials_wo_version"`
	APIURL               types.String `tfsdk:"api_url"`
	WebURL               types.String `tfsdk:"web_url"`
	AllowedOrganizations types.Set    `tfsdk:"allowed_organizations"`
	ProvisioningType     types.String `tfsdk:"provisioning_type"`
	SynchronizeGroups    types.Bool   `tfsdk:"synchronize_groups"`
	AllowUsersToSignUp   types.Bool   `tfsdk:"allow_users_to_sign_up"`
	ProjectVisibility    types.Bool   `tfsdk:"project_visibility"`
	ErrorMessage         types.String `tfsdk:"error_message"`
}

func (r *githubConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_github_configuration"
}

func (r *githubConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "GitHub authentication and provisioning, through a GitHub App. SonarQube has one such configuration; " +
			"destroying the resource deletes it. It is stored in the `sonar.auth.github.*` settings, which must not also be " +
			"managed with `sonarqube_setting`. Import ID: the configuration ID.",
		Attributes: merge(commonProvisioningAttrs("GitHub", "the users and teams of the allowed organizations"),
			secretAttrs("client_secret", "Client secret of the GitHub App.", true),
			secretAttrs("private_key", "Private key (PEM) of the GitHub App.", true),
			map[string]schema.Attribute{
				"client_id":      schema.StringAttribute{Required: true, Description: "Client ID of the GitHub App."},
				"application_id": schema.StringAttribute{Required: true, Description: "App ID of the GitHub App."},
				"api_url": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("https://api.github.com/"),
					Description: "API URL: `https://api.github.com/`, or `https://github.example.com/api/v3/` for GitHub Enterprise."},
				"web_url": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("https://github.com/"),
					Description: "Web URL: `https://github.com/`, or `https://github.example.com/` for GitHub Enterprise."},
				"allowed_organizations": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
					Default:     setdefault.StaticValue(emptyStringSet()),
					Description: "Organizations whose members can log in. Empty allows members of any organization the GitHub App is installed in."},
				"project_visibility": schema.BoolAttribute{Optional: true, Computed: true,
					Description:   "With `AUTO_PROVISIONING`, make provisioned projects public or private like their repository; otherwise they are private. Defaults to the server's choice.",
					PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
			}),
	}
}

func (r *githubConfigResource) find(ctx context.Context) (*sonarqube.GithubConfigurationResource, error) {
	res, _, err := r.c().DopTranslation.SearchGithubConfigurations(ctx)
	if err != nil || len(res.GithubConfigurations) == 0 {
		return nil, err
	}
	return res.GithubConfigurations[0], nil
}

func (r *githubConfigResource) apply(m *githubConfigModel, g *sonarqube.GithubConfigurationResource) {
	m.ID = types.StringValue(g.ID)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ApplicationID = types.StringValue(g.ApplicationID)
	m.APIURL = types.StringValue(g.APIURL)
	m.WebURL = types.StringValue(g.WebURL)
	m.AllowedOrganizations = setOfStrings(g.AllowedOrganizations)
	m.ProvisioningType = types.StringValue(g.ProvisioningType)
	m.SynchronizeGroups = types.BoolValue(g.SynchronizeGroups)
	m.AllowUsersToSignUp = types.BoolValue(g.AllowUsersToSignUp)
	m.ProjectVisibility = types.BoolValue(g.ProjectVisibility)
	m.ErrorMessage = types.StringValue(g.ErrorMessage)
	m.ClientSecretWO, m.PrivateKeyWO = types.StringNull(), types.StringNull()
}

func (r *githubConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg githubConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if existing, err := r.find(ctx); err != nil {
		apiError(&resp.Diagnostics, "Unable to read the GitHub configuration", err)
		return
	} else if existing != nil {
		singletonExists(resp, "GitHub configuration", existing.ID)
		return
	}
	var orgs []string
	resp.Diagnostics.Append(plan.AllowedOrganizations.ElementsAs(ctx, &orgs, false)...)
	body := &sonarqube.GithubConfigurationCreateRestRequest{
		Enabled:              plan.Enabled.ValueBool(),
		ClientID:             plan.ClientID.ValueString(),
		ClientSecret:         *secret(plan.ClientSecret, cfg.ClientSecretWO),
		ApplicationID:        plan.ApplicationID.ValueString(),
		PrivateKey:           *secret(plan.PrivateKey, cfg.PrivateKeyWO),
		APIURL:               plan.APIURL.ValueString(),
		WebURL:               plan.WebURL.ValueString(),
		AllowedOrganizations: append([]string{}, orgs...),
		ProvisioningType:     plan.ProvisioningType.ValueString(),
		SynchronizeGroups:    plan.SynchronizeGroups.ValueBool(),
	}
	if !plan.AllowUsersToSignUp.IsUnknown() {
		body.AllowUsersToSignUp = plan.AllowUsersToSignUp.ValueBoolPointer()
	}
	if !plan.ProjectVisibility.IsUnknown() {
		body.ProjectVisibility = plan.ProjectVisibility.ValueBoolPointer()
	}
	g, _, err := r.c().DopTranslation.CreateGithubConfiguration(ctx, &sonarqube.DopTranslationCreateGithubConfigurationOptions{Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to create the GitHub configuration", err)
		return
	}
	r.apply(&plan, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *githubConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state githubConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, _, err := r.c().DopTranslation.GetGithubConfiguration(ctx, &sonarqube.DopTranslationGetGithubConfigurationOptions{ID: state.ID.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the GitHub configuration", err)
		return
	}
	r.apply(&state, g)
	// The client ID is not returned; keep the configured one.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *githubConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg githubConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var orgs []string
	resp.Diagnostics.Append(plan.AllowedOrganizations.ElementsAs(ctx, &orgs, false)...)
	// Secrets are always sent: SonarQube may require them again when the URLs change.
	body := &sonarqube.GithubConfigurationUpdateRestRequest{
		Enabled:           plan.Enabled.ValueBoolPointer(),
		ClientID:          plan.ClientID.ValueStringPointer(),
		ClientSecret:      secret(plan.ClientSecret, cfg.ClientSecretWO),
		ApplicationID:     plan.ApplicationID.ValueStringPointer(),
		PrivateKey:        secret(plan.PrivateKey, cfg.PrivateKeyWO),
		APIURL:            plan.APIURL.ValueStringPointer(),
		WebURL:            plan.WebURL.ValueStringPointer(),
		ProvisioningType:  plan.ProvisioningType.ValueStringPointer(),
		SynchronizeGroups: plan.SynchronizeGroups.ValueBoolPointer(),
	}
	if !plan.AllowUsersToSignUp.IsUnknown() {
		body.AllowUsersToSignUp = plan.AllowUsersToSignUp.ValueBoolPointer()
	}
	if !plan.ProjectVisibility.IsUnknown() {
		body.ProjectVisibility = plan.ProjectVisibility.ValueBoolPointer()
	}
	body.AllowedOrganizations = &sonarqube.UpdateFieldListString{Value: orgs}
	g, _, err := r.c().DopTranslation.UpdateGithubConfiguration(ctx, &sonarqube.DopTranslationUpdateGithubConfigurationOptions{
		ID: state.ID.ValueString(), Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to update the GitHub configuration", err)
		return
	}
	r.apply(&plan, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *githubConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state githubConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().DopTranslation.DeleteGithubConfiguration(ctx, &sonarqube.DopTranslationDeleteGithubConfigurationOptions{ID: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete the GitHub configuration", err)
	}
}

func (r *githubConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *githubConfigResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

// ---------------------------------------------------------------------------
// sonarqube_gitlab_configuration

type gitlabConfigResource struct{ resourceBase }

func (r *gitlabConfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	requireAutoProvisioningEdition(ctx, &r.resourceBase, req, resp)
}

func newGitlabConfigResource() resource.Resource { return &gitlabConfigResource{} }

type gitlabConfigModel struct {
	ID                     types.String `tfsdk:"id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	ApplicationID          types.String `tfsdk:"application_id"`
	Secret                 types.String `tfsdk:"secret"`
	SecretWO               types.String `tfsdk:"secret_wo"`
	ProvisioningToken      types.String `tfsdk:"provisioning_token"`
	ProvisioningTokenWO    types.String `tfsdk:"provisioning_token_wo"`
	CredentialsWOVersion   types.String `tfsdk:"credentials_wo_version"`
	URL                    types.String `tfsdk:"url"`
	AllowedGroups          types.Set    `tfsdk:"allowed_groups"`
	AllowAllGroups         types.Bool   `tfsdk:"allow_all_groups"`
	ProvisioningType       types.String `tfsdk:"provisioning_type"`
	SynchronizeGroups      types.Bool   `tfsdk:"synchronize_groups"`
	AllowUsersToSignUp     types.Bool   `tfsdk:"allow_users_to_sign_up"`
	IsProvisioningTokenSet types.Bool   `tfsdk:"provisioning_token_set"`
	ErrorMessage           types.String `tfsdk:"error_message"`
}

func (r *gitlabConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gitlab_configuration"
}

func (r *gitlabConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "GitLab authentication and provisioning. SonarQube has one such configuration; destroying the resource " +
			"deletes it. It is stored in the `sonar.auth.gitlab.*` settings, which must not also be managed with " +
			"`sonarqube_setting`. Import ID: the configuration ID.",
		Attributes: merge(commonProvisioningAttrs("GitLab", "the members of the allowed groups"),
			secretAttrs("secret", "Secret of the GitLab application.", true),
			secretAttrs("provisioning_token", "GitLab token with `read_api` scope, needed by `AUTO_PROVISIONING`.", false),
			map[string]schema.Attribute{
				"application_id": schema.StringAttribute{Required: true, Description: "Application ID of the GitLab application."},
				"url": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("https://gitlab.com"),
					Description: "URL of the GitLab instance."},
				"allowed_groups": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
					Default: setdefault.StaticValue(emptyStringSet()),
					Description: "Root groups whose members (subgroups included) can log in, and are provisioned with `AUTO_PROVISIONING`. " +
						"`AUTO_PROVISIONING` needs at least one, unless `allow_all_groups` is set."},
				"allow_all_groups": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
					Description: "With `AUTO_PROVISIONING`, provision every group the provisioning token can see and ignore `allowed_groups`. Not allowed with gitlab.com."},
				"provisioning_token_set": schema.BoolAttribute{Computed: true, Description: "Whether SonarQube has a provisioning token."},
			}),
	}
}

func (r *gitlabConfigResource) find(ctx context.Context) (*sonarqube.GitlabConfigurationRestResponseForAdmins, error) {
	res, _, err := r.c().DopTranslation.SearchGitlabConfigurations(ctx)
	if err != nil || len(res.GitlabConfigurations) == 0 {
		return nil, err
	}
	return res.GitlabConfigurations[0], nil
}

func (r *gitlabConfigResource) apply(m *gitlabConfigModel, g *sonarqube.GitlabConfigurationRestResponseForAdmins) {
	m.ID = types.StringValue(g.ID)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ApplicationID = types.StringValue(g.ApplicationID)
	m.URL = types.StringValue(g.URL)
	m.AllowedGroups = setOfStrings(g.AllowedGroups)
	m.AllowAllGroups = types.BoolValue(g.AllowAllGroups)
	m.ProvisioningType = types.StringValue(g.ProvisioningType)
	m.SynchronizeGroups = types.BoolValue(g.SynchronizeGroups)
	m.AllowUsersToSignUp = types.BoolValue(g.AllowUsersToSignUp)
	m.IsProvisioningTokenSet = types.BoolValue(g.IsProvisioningTokenSet)
	m.ErrorMessage = types.StringValue(g.ErrorMessage)
	m.SecretWO, m.ProvisioningTokenWO = types.StringNull(), types.StringNull()
}

func (r *gitlabConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg gitlabConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if existing, err := r.find(ctx); err != nil {
		apiError(&resp.Diagnostics, "Unable to read the GitLab configuration", err)
		return
	} else if existing != nil {
		singletonExists(resp, "GitLab configuration", existing.ID)
		return
	}
	var groups []string
	resp.Diagnostics.Append(plan.AllowedGroups.ElementsAs(ctx, &groups, false)...)
	body := &sonarqube.GitlabConfigurationCreateRestRequest{
		Enabled:           plan.Enabled.ValueBool(),
		ApplicationID:     plan.ApplicationID.ValueString(),
		Secret:            *secret(plan.Secret, cfg.SecretWO),
		ProvisioningToken: secret(plan.ProvisioningToken, cfg.ProvisioningTokenWO),
		URL:               plan.URL.ValueString(),
		AllowedGroups:     append([]string{}, groups...),
		AllowAllGroups:    plan.AllowAllGroups.ValueBoolPointer(),
		ProvisioningType:  plan.ProvisioningType.ValueString(),
		SynchronizeGroups: plan.SynchronizeGroups.ValueBool(),
	}
	if !plan.AllowUsersToSignUp.IsUnknown() {
		body.AllowUsersToSignUp = plan.AllowUsersToSignUp.ValueBoolPointer()
	}
	g, _, err := r.c().DopTranslation.CreateGitlabConfiguration(ctx, &sonarqube.DopTranslationCreateGitlabConfigurationOptions{Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to create the GitLab configuration", err)
		return
	}
	r.apply(&plan, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gitlabConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gitlabConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, _, err := r.c().DopTranslation.GetGitlabConfiguration(ctx, &sonarqube.DopTranslationGetGitlabConfigurationOptions{ID: state.ID.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the GitLab configuration", err)
		return
	}
	r.apply(&state, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gitlabConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg gitlabConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var groups []string
	resp.Diagnostics.Append(plan.AllowedGroups.ElementsAs(ctx, &groups, false)...)
	body := &sonarqube.GitlabConfigurationUpdateRestRequest{
		Enabled:           plan.Enabled.ValueBoolPointer(),
		ApplicationID:     plan.ApplicationID.ValueStringPointer(),
		Secret:            secret(plan.Secret, cfg.SecretWO),
		ProvisioningToken: secret(plan.ProvisioningToken, cfg.ProvisioningTokenWO),
		URL:               plan.URL.ValueStringPointer(),
		AllowAllGroups:    plan.AllowAllGroups.ValueBoolPointer(),
		ProvisioningType:  plan.ProvisioningType.ValueStringPointer(),
		SynchronizeGroups: plan.SynchronizeGroups.ValueBoolPointer(),
	}
	if !plan.AllowUsersToSignUp.IsUnknown() {
		body.AllowUsersToSignUp = plan.AllowUsersToSignUp.ValueBoolPointer()
	}
	body.AllowedGroups = &sonarqube.UpdateFieldListString{Value: groups}
	g, _, err := r.c().DopTranslation.UpdateGitlabConfiguration(ctx, &sonarqube.DopTranslationUpdateGitlabConfigurationOptions{
		ID: state.ID.ValueString(), Body: body})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to update the GitLab configuration", err)
		return
	}
	r.apply(&plan, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gitlabConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gitlabConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().DopTranslation.DeleteGitlabConfiguration(ctx, &sonarqube.DopTranslationDeleteGitlabConfigurationOptions{ID: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete the GitLab configuration", err)
	}
}

func (r *gitlabConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *gitlabConfigResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

// ---------------------------------------------------------------------------
// sonarqube_scim_provisioning

var _ resource.ResourceWithModifyPlan = (*scimResource)(nil)

type scimResource struct{ resourceBase }

func newScimResource() resource.Resource { return &scimResource{} }

type scimModel struct {
	ID types.String `tfsdk:"id"`
}

const scimID = "scim"

func (r *scimResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_provisioning"
}

func (r *scimResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "Enables SCIM provisioning of users and groups from an identity provider, while the resource exists. " +
			"SCIM makes users and groups read-only in SonarQube: manage them in the identity provider instead, and remove " +
			"sonarqube_user, sonarqube_group and sonarqube_group_member resources first. It needs Enterprise edition or " +
			"above, and SAML authentication configured first. Import ID: `" + scimID + "`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always `" + scimID + "`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *scimResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || !r.configured() {
		return
	}
	r.p.requireEdition(ctx, &resp.Diagnostics, "SCIM provisioning", "enterprise")
}

func (r *scimResource) Create(ctx context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	// SonarQube answers a bare HTTP 500 when SAML is off; say why instead.
	saml, err := readSettings(ctx, r.c(), "", "sonar.auth.saml.enabled")
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the SAML settings", err)
		return
	}
	if len(saml.Settings) == 0 || saml.Settings[0].Value != "true" {
		resp.Diagnostics.AddError("SAML is not enabled",
			"SCIM provisioning needs SAML authentication: set sonar.auth.saml.enabled to true, with the other sonar.auth.saml.* "+
				"settings, e.g. with sonarqube_setting and sonarqube_secured_setting resources this resource depends on.")
		return
	}
	if _, err := r.c().ScimManagement.Enable(ctx); err != nil {
		apiError(&resp.Diagnostics, "Unable to enable SCIM provisioning", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &scimModel{ID: types.StringValue(scimID)})...)
}

func (r *scimResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	s, _, err := r.c().ScimManagement.Status(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the SCIM status", err)
		return
	}
	if !s.Enabled { // disabled outside Terraform
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &scimModel{ID: types.StringValue(scimID)})...)
}

func (r *scimResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {}

func (r *scimResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	if _, err := r.c().ScimManagement.Disable(ctx); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to disable SCIM provisioning", err)
	}
}

func (r *scimResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != scimID {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("The import ID of sonarqube_scim_provisioning is %q.", scimID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), scimID)...)
}

func (r *scimResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
