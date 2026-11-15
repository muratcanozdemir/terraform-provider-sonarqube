package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// almDefinitions returns api/alm_settings/list_definitions grouped by
// platform. The typed model leaves most platforms as raw JSON.
func almDefinitions(ctx context.Context, c *sonarqube.Client) (map[string][]map[string]any, error) {
	_, raw, err := c.AlmSettings.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal(raw.Body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func almDefinition(ctx context.Context, c *sonarqube.Client, platform, key string) (map[string]any, error) {
	defs, err := almDefinitions(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, d := range defs[platform] {
		if d["key"] == key {
			return d, nil
		}
	}
	return nil, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// ---------------------------------------------------------------------------
// ALM settings: one implementation, parameterised per platform.

type almField struct {
	attr      string // Terraform attribute
	param     string // API parameter (create and update)
	readKey   string // key in list_definitions, "" when never returned (secrets)
	required  bool
	sensitive bool
	desc      string
}

type almKind struct {
	typeName string // e.g. "_alm_github"
	platform string // key in list_definitions
	title    string
	fields   []almField
	create   string // API paths
	update   string
}

type almResource struct {
	resourceBase
	kind almKind
}

func (r *almResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.kind.typeName
}

func (r *almResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, Description: "The setting key."},
		"key": schema.StringAttribute{Required: true, Description: "Unique key of the setting. Changing it renames the setting in place, keeping its project bindings.",
			Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
	}
	for _, f := range r.kind.fields {
		a := schema.StringAttribute{Required: f.required, Optional: !f.required, Sensitive: f.sensitive, Description: f.desc}
		if f.sensitive {
			// Either the value, stored in state, or its write-only form.
			wo := f.attr + "_wo"
			a.Required, a.Optional = false, true
			a.Description += " Stored in state; prefer `" + wo + "`. SonarQube does not return it, so drift cannot be detected."
			both := []path.Expression{path.MatchRoot(f.attr), path.MatchRoot(wo)}
			one := stringvalidator.ConflictsWith(path.MatchRoot(wo))
			if f.required {
				one = stringvalidator.ExactlyOneOf(both...)
			}
			a.Validators = []validator.String{one}
			attrs[wo] = schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Write-only form of `" + f.attr + "`, never stored in state (Terraform 1.11+). " +
					"Change `credentials_wo_version` to apply a new value.",
				Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("credentials_wo_version"))}}
			if f.required {
				attrs[wo] = withValidator(attrs[wo].(schema.StringAttribute), stringvalidator.ExactlyOneOf(both...))
			}
		}
		attrs[f.attr] = a
	}
	if r.hasSecrets() {
		attrs["credentials_wo_version"] = schema.StringAttribute{Optional: true,
			Description: "Any value; changing it applies the current values of the `_wo` arguments."}
	}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A " + r.kind.title + " DevOps platform setting. Every attribute changes in place. Destroying a setting that " +
			"projects are still bound to is refused, because SonarQube would remove their bindings with it. Import ID: the key.",
		Attributes: attrs,
	}
}

var _ resource.ResourceWithModifyPlan = (*almResource)(nil)

// ModifyPlan keeps the ID known when the key does not change.
func (r *almResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var oldKey, newKey, id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("key"), &oldKey)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("key"), &newKey)...)
	if oldKey.Equal(newKey) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), id)...)
	}
}

func (r *almResource) values(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) map[string]types.String {
	out := map[string]types.String{}
	for _, a := range append([]string{"id", "key"}, r.attrs()...) {
		var v types.String
		diags.Append(get(ctx, path.Root(a), &v)...)
		out[a] = v
	}
	return out
}

func (r *almResource) attrs() []string {
	var out []string
	for _, f := range r.kind.fields {
		out = append(out, f.attr)
		if f.sensitive {
			out = append(out, f.attr+"_wo")
		}
	}
	if r.hasSecrets() {
		out = append(out, "credentials_wo_version")
	}
	return out
}

func (r *almResource) hasSecrets() bool {
	for _, f := range r.kind.fields {
		if f.sensitive {
			return true
		}
	}
	return false
}

func withValidator(a schema.StringAttribute, v validator.String) schema.StringAttribute {
	a.Validators = append(a.Validators, v)
	return a
}

func (r *almResource) store(ctx context.Context, set func(context.Context, path.Path, any) diag.Diagnostics, vals map[string]types.String, diags *diag.Diagnostics) {
	for a, v := range vals {
		diags.Append(set(ctx, path.Root(a), v)...)
	}
}

// send calls create (oldKey == "") or update. Secrets are always sent:
// SonarQube requires them again whenever it considers the URL changed (so
// stored credentials cannot be redirected to another host), and it compares
// URLs after normalising them.
// cfg supplies write-only values, which are never in the plan.
func (r *almResource) send(ctx context.Context, oldKey string, plan, cfg map[string]types.String) error {
	path := r.kind.create
	form := map[string][]string{"key": {plan["key"].ValueString()}}
	if oldKey != "" {
		path = r.kind.update
		form["key"] = []string{oldKey}
		if nk := plan["key"].ValueString(); nk != oldKey {
			form["newKey"] = []string{nk}
		}
	}
	for _, f := range r.kind.fields {
		v := plan[f.attr]
		if f.sensitive && v.IsNull() {
			v = cfg[f.attr+"_wo"]
		}
		if v.IsNull() || v.IsUnknown() {
			continue
		}
		form[f.param] = []string{v.ValueString()}
	}
	return rawPost(ctx, r.c(), path, form)
}

func (r *almResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	plan := r.values(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	cfg := r.values(ctx, req.Config.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.send(ctx, "", plan, cfg); err != nil {
		if hasMessage(err, "is already defined") {
			resp.Diagnostics.AddError("Unable to create "+r.kind.title+" setting", explain(err)+
				"\n\nCommunity Build and Developer edition allow one setting per DevOps platform; several need Enterprise edition or above.")
			return
		}
		createError(&resp.Diagnostics, r.kind.title+" setting", plan["key"].ValueString(), err)
		return
	}
	plan["id"] = plan["key"]
	r.store(ctx, resp.State.SetAttribute, plan, &resp.Diagnostics)
}

func (r *almResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	state := r.values(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state["id"].ValueString()
	d, err := almDefinition(ctx, r.c(), r.kind.platform, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read DevOps platform settings", err)
		return
	}
	if d == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state["key"] = types.StringValue(key)
	for _, f := range r.kind.fields {
		if f.readKey == "" {
			continue
		}
		v := str(d[f.readKey])
		// SonarQube drops a trailing slash from URLs; keep the configured form.
		if old := state[f.attr].ValueString(); strings.TrimSuffix(old, "/") == strings.TrimSuffix(v, "/") {
			v = old
		}
		state[f.attr] = keepNullIfEmpty(state[f.attr], v)
	}
	r.store(ctx, resp.State.SetAttribute, state, &resp.Diagnostics)
}

func (r *almResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	plan := r.values(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	state := r.values(ctx, req.State.GetAttribute, &resp.Diagnostics)
	cfg := r.values(ctx, req.Config.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.send(ctx, state["id"].ValueString(), plan, cfg); err != nil {
		apiError(&resp.Diagnostics, "Unable to update "+r.kind.title+" setting "+state["id"].ValueString(), err)
		return
	}
	plan["id"] = plan["key"]
	r.store(ctx, resp.State.SetAttribute, plan, &resp.Diagnostics)
}

func (r *almResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	state := r.values(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state["id"].ValueString()
	n, _, err := r.c().AlmSettings.CountBinding(ctx, &sonarqube.AlmSettingsCountBindingOptions{AlmSetting: key})
	if isGone(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to count bindings of "+key, err)
		return
	}
	if n.Projects > 0 {
		resp.Diagnostics.AddError("DevOps platform setting is in use",
			fmt.Sprintf("%d project(s) are bound to %q; deleting it would remove their bindings. Unbind them first (destroy their binding resources).", n.Projects, key))
		return
	}
	if _, err := r.c().AlmSettings.Delete(ctx, &sonarqube.AlmSettingsDeleteOptions{Key: key}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete "+key, err)
	}
}

func (r *almResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func newAlmGithubResource() resource.Resource {
	return &almResource{kind: almKind{typeName: "_alm_github", platform: "github", title: "GitHub",
		create: "/api/alm_settings/create_github", update: "/api/alm_settings/update_github",
		fields: []almField{
			{attr: "url", param: "url", readKey: "url", required: true, desc: "GitHub API URL, e.g. `https://api.github.com/`."},
			{attr: "app_id", param: "appId", readKey: "appId", required: true, desc: "GitHub App ID."},
			{attr: "client_id", param: "clientId", readKey: "clientId", required: true, desc: "GitHub App client ID."},
			{attr: "client_secret", param: "clientSecret", required: true, sensitive: true, desc: "GitHub App client secret."},
			{attr: "private_key", param: "privateKey", required: true, sensitive: true, desc: "GitHub App private key (PEM)."},
			{attr: "webhook_secret", param: "webhookSecret", sensitive: true, desc: "GitHub App webhook secret."},
		}}}
}

func newAlmGitlabResource() resource.Resource {
	return &almResource{kind: almKind{typeName: "_alm_gitlab", platform: "gitlab", title: "GitLab",
		create: "/api/alm_settings/create_gitlab", update: "/api/alm_settings/update_gitlab",
		fields: []almField{
			{attr: "url", param: "url", readKey: "url", required: true, desc: "GitLab API URL, e.g. `https://gitlab.com/api/v4`."},
			{attr: "personal_access_token", param: "personalAccessToken", required: true, sensitive: true, desc: "GitLab personal access token."},
		}}}
}

func newAlmAzureResource() resource.Resource {
	return &almResource{kind: almKind{typeName: "_alm_azure", platform: "azure", title: "Azure DevOps",
		create: "/api/alm_settings/create_azure", update: "/api/alm_settings/update_azure",
		fields: []almField{
			{attr: "url", param: "url", readKey: "url", required: true, desc: "Azure DevOps collection URL."},
			{attr: "personal_access_token", param: "personalAccessToken", required: true, sensitive: true, desc: "Azure DevOps personal access token."},
		}}}
}

func newAlmBitbucketResource() resource.Resource {
	return &almResource{kind: almKind{typeName: "_alm_bitbucket", platform: "bitbucket", title: "Bitbucket Server",
		create: "/api/alm_settings/create_bitbucket", update: "/api/alm_settings/update_bitbucket",
		fields: []almField{
			{attr: "url", param: "url", readKey: "url", required: true, desc: "Bitbucket Server URL."},
			{attr: "personal_access_token", param: "personalAccessToken", required: true, sensitive: true, desc: "Bitbucket personal access token."},
		}}}
}

func newAlmBitbucketCloudResource() resource.Resource {
	return &almResource{kind: almKind{typeName: "_alm_bitbucket_cloud", platform: "bitbucketcloud", title: "Bitbucket Cloud",
		create: "/api/alm_settings/create_bitbucketcloud", update: "/api/alm_settings/update_bitbucketcloud",
		fields: []almField{
			{attr: "workspace", param: "workspace", readKey: "workspace", required: true, desc: "Bitbucket Cloud workspace ID."},
			{attr: "client_id", param: "clientId", readKey: "clientId", required: true, desc: "OAuth consumer key."},
			{attr: "client_secret", param: "clientSecret", required: true, sensitive: true, desc: "OAuth consumer secret."},
		}}}
}

// ---------------------------------------------------------------------------
// Project bindings: one implementation, parameterised per platform.

type bindingField struct {
	attr     string
	param    string // set_*_binding parameter
	readKey  string // get_binding key
	boolean  bool   // Terraform bool (else string)
	strBool  bool   // string "true"/"false" (kept for compatibility)
	required bool
	def      string // default for optional booleans
	desc     string
}

type bindingKind struct {
	typeName string
	alm      string // get_binding "alm"
	title    string
	setPath  string
	fields   []bindingField
}

var _ resource.ResourceWithModifyPlan = (*bindingResource)(nil)

type bindingResource struct {
	resourceBase
	kind bindingKind
}

func (r *bindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + r.kind.typeName
}

func (r *bindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project": schema.StringAttribute{Required: true, Description: "Project key.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"alm_setting": schema.StringAttribute{Required: true, Description: "Key of the " + r.kind.title + " setting."},
	}
	for _, f := range r.kind.fields {
		switch {
		case f.boolean:
			attrs[f.attr] = schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(f.def == "true"), Description: f.desc}
		case f.strBool:
			attrs[f.attr] = schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(f.def), Description: f.desc,
				Validators: []validator.String{stringvalidator.OneOf("true", "false")}}
		default:
			attrs[f.attr] = schema.StringAttribute{Required: f.required, Optional: !f.required, Description: f.desc}
		}
	}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Binds a project to a " + r.kind.title + " repository. Requires Developer edition or above. Changes apply in place. Import ID: the project key.",
		Attributes:  attrs,
	}
}

// ModifyPlan rejects the binding at plan time on editions without bindings,
// rather than failing half-way through an apply.
func (r *bindingResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || !r.configured() {
		return
	}
	r.p.requireEdition(ctx, &resp.Diagnostics, r.kind.title+" project binding", "developer")
}

func (r *bindingResource) names() []string {
	out := []string{"id", "project", "alm_setting"}
	for _, f := range r.kind.fields {
		out = append(out, f.attr)
	}
	return out
}

func (r *bindingResource) load(ctx context.Context, get func(context.Context, path.Path, any) diag.Diagnostics, diags *diag.Diagnostics) map[string]attr.Value {
	out := map[string]attr.Value{}
	for _, f := range r.kind.fields {
		if f.boolean {
			var v types.Bool
			diags.Append(get(ctx, path.Root(f.attr), &v)...)
			out[f.attr] = v
		}
	}
	for _, a := range r.names() {
		if _, done := out[a]; done {
			continue
		}
		var v types.String
		diags.Append(get(ctx, path.Root(a), &v)...)
		out[a] = v
	}
	return out
}

func (r *bindingResource) store(ctx context.Context, set func(context.Context, path.Path, any) diag.Diagnostics, vals map[string]attr.Value, diags *diag.Diagnostics) {
	for a, v := range vals {
		diags.Append(set(ctx, path.Root(a), v)...)
	}
}

func (r *bindingResource) set(ctx context.Context, vals map[string]attr.Value) error {
	form := map[string][]string{
		"project":    {vals["project"].(types.String).ValueString()},
		"almSetting": {vals["alm_setting"].(types.String).ValueString()},
	}
	for _, f := range r.kind.fields {
		switch v := vals[f.attr].(type) {
		case types.Bool:
			if !v.IsNull() && !v.IsUnknown() {
				form[f.param] = []string{fmt.Sprint(v.ValueBool())}
			}
		case types.String:
			if !v.IsNull() && !v.IsUnknown() {
				form[f.param] = []string{v.ValueString()}
			}
		}
	}
	return rawPost(ctx, r.c(), r.kind.setPath, form)
}

func (r *bindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	vals := r.load(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, vals); err != nil {
		apiError(&resp.Diagnostics, "Unable to bind project", err)
		return
	}
	vals["id"] = vals["project"]
	r.store(ctx, resp.State.SetAttribute, vals, &resp.Diagnostics)
}

func (r *bindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	vals := r.load(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	project := vals["project"].(types.String).ValueString()
	if project == "" {
		project, _, _ = strings.Cut(vals["id"].(types.String).ValueString(), "/") // import or legacy ID
	}
	_, raw, err := r.c().AlmSettings.GetBinding(ctx, &sonarqube.AlmSettingsGetBindingOptions{Project: project})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read binding of "+project, err)
		return
	}
	var b map[string]any
	if err := json.Unmarshal(raw.Body, &b); err != nil {
		resp.Diagnostics.AddError("Unable to decode binding", err.Error())
		return
	}
	if str(b["alm"]) != r.kind.alm {
		resp.State.RemoveResource(ctx) // now bound to another platform
		return
	}
	vals["id"], vals["project"] = types.StringValue(project), types.StringValue(project)
	vals["alm_setting"] = types.StringValue(str(b["key"]))
	for _, f := range r.kind.fields {
		v, present := b[f.readKey]
		if f.readKey == "" || !present {
			continue
		}
		switch {
		case f.boolean:
			vals[f.attr] = types.BoolValue(str(v) == "true")
		default:
			vals[f.attr] = types.StringValue(str(v))
		}
	}
	r.store(ctx, resp.State.SetAttribute, vals, &resp.Diagnostics)
}

func (r *bindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	vals := r.load(ctx, req.Plan.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, vals); err != nil {
		apiError(&resp.Diagnostics, "Unable to update binding", err)
		return
	}
	vals["id"] = vals["project"]
	r.store(ctx, resp.State.SetAttribute, vals, &resp.Diagnostics)
}

func (r *bindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	vals := r.load(ctx, req.State.GetAttribute, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	project := vals["project"].(types.String).ValueString()
	if _, err := r.c().AlmSettings.DeleteBinding(ctx, &sonarqube.AlmSettingsDeleteBindingOptions{Project: project}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to unbind "+project, err)
	}
}

func (r *bindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, _, _ := strings.Cut(req.ID, "/")
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), project)...)
}

var monorepoStr = bindingField{attr: "monorepo", param: "monorepo", readKey: "monorepo", strBool: true, def: "false",
	desc: "`true` if the repository holds several SonarQube projects."}

func newGithubBindingResource() resource.Resource {
	return &bindingResource{kind: bindingKind{typeName: "_github_binding", alm: "github", title: "GitHub", setPath: "/api/alm_settings/set_github_binding",
		fields: []bindingField{
			{attr: "repository", param: "repository", readKey: "repository", required: true, desc: "Repository, as `owner/name`."},
			monorepoStr,
			{attr: "summary_comment_enabled", param: "summaryCommentEnabled", readKey: "summaryCommentEnabled", strBool: true, def: "true",
				desc: "Post a summary comment on pull requests."},
		}}}
}

func newGitlabBindingResource() resource.Resource {
	return &bindingResource{kind: bindingKind{typeName: "_gitlab_binding", alm: "gitlab", title: "GitLab", setPath: "/api/alm_settings/set_gitlab_binding",
		fields: []bindingField{
			{attr: "repository", param: "repository", readKey: "repository", required: true, desc: "GitLab project ID."},
			monorepoStr,
		}}}
}

func newBitbucketBindingResource() resource.Resource {
	return &bindingResource{kind: bindingKind{typeName: "_bitbucket_binding", alm: "bitbucket", title: "Bitbucket Server", setPath: "/api/alm_settings/set_bitbucket_binding",
		fields: []bindingField{
			{attr: "repository", param: "repository", readKey: "repository", required: true, desc: "Bitbucket project key."},
			{attr: "slug", param: "slug", readKey: "slug", required: true, desc: "Repository slug."},
			monorepoStr,
		}}}
}

func newBitbucketCloudBindingResource() resource.Resource {
	return &bindingResource{kind: bindingKind{typeName: "_bitbucket_cloud_binding", alm: "bitbucketcloud", title: "Bitbucket Cloud", setPath: "/api/alm_settings/set_bitbucketcloud_binding",
		fields: []bindingField{
			{attr: "repository", param: "repository", readKey: "repository", required: true, desc: "Repository slug."},
			monorepoStr,
		}}}
}

func newAzureBindingResource() resource.Resource {
	return &bindingResource{kind: bindingKind{typeName: "_azure_binding", alm: "azure", title: "Azure DevOps", setPath: "/api/alm_settings/set_azure_binding",
		fields: []bindingField{
			{attr: "project_name", param: "projectName", readKey: "slug", required: true, desc: "Azure DevOps project name."},
			{attr: "repository_name", param: "repositoryName", readKey: "repository", required: true, desc: "Repository name."},
			{attr: "monorepo", param: "monorepo", readKey: "monorepo", boolean: true, def: "false", desc: "Whether the repository holds several SonarQube projects."},
		}}}
}

func (r *almResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *bindingResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
