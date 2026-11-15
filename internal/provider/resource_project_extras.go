package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
// sonarqube_webhook

type webhookResource struct{ resourceBase }

func newWebhookResource() resource.Resource { return &webhookResource{} }

type webhookModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	URL     types.String `tfsdk:"url"`
	Secret  types.String `tfsdk:"secret"`
	Project types.String `tfsdk:"project"`

	SecretWO        types.String `tfsdk:"secret_wo"`
	SecretWOVersion types.String `tfsdk:"secret_wo_version"`
}

// secret returns the secret to send: secret, or else secret_wo from the
// configuration (write-only values are never in the plan).
func (m *webhookModel) secret(cfg *webhookModel) string {
	if !m.Secret.IsNull() {
		return m.Secret.ValueString()
	}
	return cfg.SecretWO.ValueString()
}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A global or project webhook. Import ID: `key` or `key/project`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, Description: "Webhook name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100)}},
			"url": schema.StringAttribute{Required: true, Description: "Endpoint receiving the payload.",
				Validators: []validator.String{stringvalidator.RegexMatches(regexpMust(`^https?://`), "must be an http(s) URL")}},
			"secret": schema.StringAttribute{Optional: true, Sensitive: true,
				Description: "Secret used to sign payloads (HMAC-SHA256), 16 to 200 characters. SonarQube does not return it, so only its " +
					"presence is checked. Removing it from the configuration removes it from the webhook.",
				Validators: []validator.String{stringvalidator.LengthBetween(16, 200),
					stringvalidator.ConflictsWith(path.MatchRoot("secret_wo"))}},
			"secret_wo": schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
				Description: "Write-only secret, never stored in state (Terraform 1.11+). Change `secret_wo_version` to apply a new one.",
				Validators: []validator.String{stringvalidator.LengthBetween(16, 200),
					stringvalidator.AlsoRequires(path.MatchRoot("secret_wo_version"))}},
			"secret_wo_version": schema.StringAttribute{Optional: true,
				Description: "Any value; changing it applies the current `secret_wo`. Removing `secret_wo` and this removes the secret."},
			"project": schema.StringAttribute{Optional: true, Description: "Project key for a project webhook.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		},
	}
}

func (r *webhookResource) find(ctx context.Context, project, key string) (*sonarqube.WebhooksListResponseWebhook, error) {
	res, _, err := r.c().Webhooks.List(ctx, &sonarqube.WebhooksListOptions{Project: project})
	if err != nil {
		return nil, err
	}
	for _, w := range res.Webhooks {
		if w.Key == key {
			return w, nil
		}
	}
	return nil, nil
}

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().Webhooks.Create(ctx, &sonarqube.WebhooksCreateOptions{Name: plan.Name.ValueString(), URL: plan.URL.ValueString(),
		Secret: plan.secret(&cfg), Project: plan.Project.ValueString()})
	if err != nil {
		createError(&resp.Diagnostics, "webhook", "", err)
		return
	}
	plan.ID = types.StringValue(res.Webhook.Key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w, err := r.find(ctx, state.Project.ValueString(), state.ID.ValueString())
	if isGone(err) || (err == nil && w == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read webhooks", err)
		return
	}
	state.Name = types.StringValue(w.Name)
	state.URL = types.StringValue(w.URL)
	switch {
	case !w.HasSecret:
		state.Secret = types.StringNull()
	case state.Secret.IsNull() && state.SecretWOVersion.IsNull():
		// A secret was set outside Terraform; force an update that removes it.
		state.Secret = types.StringValue("(set outside Terraform)")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, cfg webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A blank secret removes it; the typed options cannot send a blank value.
	form := url.Values{"webhook": {state.ID.ValueString()}, "name": {plan.Name.ValueString()}, "url": {plan.URL.ValueString()},
		"secret": {plan.secret(&cfg)}}
	if err := rawPost(ctx, r.c(), "/api/webhooks/update", form); err != nil {
		apiError(&resp.Diagnostics, "Unable to update webhook "+state.Name.ValueString(), err)
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c().Webhooks.Delete(ctx, &sonarqube.WebhooksDeleteOptions{Webhook: state.ID.ValueString()}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete webhook "+state.Name.ValueString(), err)
	}
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, project, _ := strings.Cut(req.ID, "/")
	w, err := r.find(ctx, project, key)
	if err != nil || w == nil {
		resp.Diagnostics.AddError("Webhook not found", fmt.Sprintf("No webhook %q (use `key` or `key/project` for project webhooks): %v", key, err))
		return
	}
	m := webhookModel{ID: types.StringValue(key), Name: types.StringValue(w.Name), URL: types.StringValue(w.URL),
		Secret: types.StringNull(), Project: strOrNull(project), SecretWO: types.StringNull(), SecretWOVersion: types.StringNull()}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_new_code_periods

var _ resource.ResourceWithValidateConfig = (*newCodePeriodsResource)(nil)

type newCodePeriodsResource struct{ resourceBase }

func newNewCodePeriodsResource() resource.Resource { return &newCodePeriodsResource{} }

type newCodePeriodsModel struct {
	ID      types.String `tfsdk:"id"`
	Project types.String `tfsdk:"project"`
	Branch  types.String `tfsdk:"branch"`
	Type    types.String `tfsdk:"type"`
	Value   types.String `tfsdk:"value"`
}

func (r *newCodePeriodsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_new_code_periods"
}

func (r *newCodePeriodsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "The new code definition, globally, for a project or for a branch. Destroying it reverts to the inherited definition " +
			"(the global one reverts to PREVIOUS_VERSION). Import ID: `global`, `project` or `project/branch`.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project": schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Project key. Omit for the global definition."},
			"branch": schema.StringAttribute{Optional: true, PlanModifiers: replace, Description: "Branch name. Requires project.",
				Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("project"))}},
			"type": schema.StringAttribute{Required: true, Description: "`PREVIOUS_VERSION`, `NUMBER_OF_DAYS`, `REFERENCE_BRANCH` or `SPECIFIC_ANALYSIS`.",
				Validators: []validator.String{stringvalidator.OneOf("PREVIOUS_VERSION", "NUMBER_OF_DAYS", "REFERENCE_BRANCH", "SPECIFIC_ANALYSIS")}},
			"value": schema.StringAttribute{Optional: true,
				Description: "Days for NUMBER_OF_DAYS, branch for REFERENCE_BRANCH, analysis key for SPECIFIC_ANALYSIS."},
		},
	}
}

func (r *newCodePeriodsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg newCodePeriodsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if cfg.Type.IsUnknown() || cfg.Value.IsUnknown() {
		return
	}
	switch t := cfg.Type.ValueString(); {
	case t == "PREVIOUS_VERSION" && !cfg.Value.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("value"), "Unexpected value", "PREVIOUS_VERSION takes no value.")
	case t != "PREVIOUS_VERSION" && cfg.Value.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("value"), "Missing value", t+" needs a value.")
	case t == "SPECIFIC_ANALYSIS" && cfg.Branch.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("branch"), "Missing branch", "SPECIFIC_ANALYSIS applies to a branch.")
	case (t == "REFERENCE_BRANCH" || t == "SPECIFIC_ANALYSIS") && cfg.Project.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("project"), "Missing project", t+" cannot be set globally.")
	}
}

func (m *newCodePeriodsModel) id() types.String {
	switch {
	case m.Project.IsNull():
		return types.StringValue("global")
	case m.Branch.IsNull():
		return m.Project
	}
	return types.StringValue(m.Project.ValueString() + "/" + m.Branch.ValueString())
}

type apiNCP struct {
	ProjectKey string `json:"projectKey"`
	BranchKey  string `json:"branchKey"`
	Type       string `json:"type"`
	Value      string `json:"value"`
	Inherited  bool   `json:"inherited"`
}

// show returns the definition set at the resource's level, or nil when the
// level only inherits. The typed client model lacks `value`, so raw bodies
// are decoded.
func (r *newCodePeriodsResource) show(ctx context.Context, project, branch string) (*apiNCP, error) {
	_, raw, err := r.c().NewCodePeriods.Show(ctx, &sonarqube.NewCodePeriodsShowOptions{Project: project, Branch: branch})
	if err != nil {
		return nil, err
	}
	var v apiNCP
	if err := json.Unmarshal(raw.Body, &v); err != nil {
		return nil, err
	}
	if project != "" && branch == "" && v.Inherited {
		// Without branch support (Community Build) a project-level
		// definition is stored on the main branch, and project-level show
		// keeps reporting the inherited value.
		return r.mainBranchNCP(ctx, project)
	}
	if v.Inherited {
		return nil, nil
	}
	return &v, nil
}

func (r *newCodePeriodsResource) mainBranchNCP(ctx context.Context, project string) (*apiNCP, error) {
	branches, _, err := r.c().ProjectBranches.List(ctx, &sonarqube.ProjectBranchesListOptions{Project: project})
	if err != nil {
		return nil, err
	}
	main := ""
	for _, b := range branches.Branches {
		if b.IsMain {
			main = b.Name
		}
	}
	_, raw, err := r.c().NewCodePeriods.List(ctx, &sonarqube.NewCodePeriodsListOptions{Project: project})
	if err != nil {
		return nil, err
	}
	var list struct {
		NewCodePeriods []apiNCP `json:"newCodePeriods"`
	}
	if err := json.Unmarshal(raw.Body, &list); err != nil {
		return nil, err
	}
	for _, p := range list.NewCodePeriods {
		if p.BranchKey == main && !p.Inherited {
			return &p, nil
		}
	}
	return nil, nil
}

func (r *newCodePeriodsResource) set(ctx context.Context, m *newCodePeriodsModel) error {
	_, err := r.c().NewCodePeriods.Set(ctx, &sonarqube.NewCodePeriodsSetOptions{Project: m.Project.ValueString(), Branch: m.Branch.ValueString(),
		Type: m.Type.ValueString(), Value: m.Value.ValueString()})
	return err
}

func (r *newCodePeriodsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan newCodePeriodsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the new code definition", err)
		return
	}
	plan.ID = plan.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *newCodePeriodsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state newCodePeriodsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := state.Project.ValueString()
	if project != "" {
		// show answers 200 for a project that does not exist; check it.
		if _, err := showComponent(ctx, r.c(), project); isGone(err) {
			resp.State.RemoveResource(ctx)
			return
		} else if err != nil {
			apiError(&resp.Diagnostics, "Unable to read project "+project, err)
			return
		}
	}
	v, err := r.show(ctx, project, state.Branch.ValueString())
	if isGone(err) || (err == nil && v == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the new code definition", err)
		return
	}
	state.Type = types.StringValue(v.Type)
	state.Value = strOrNull(v.Value)
	state.ID = state.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *newCodePeriodsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan newCodePeriodsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the new code definition", err)
		return
	}
	plan.ID = plan.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *newCodePeriodsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state newCodePeriodsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var err error
	if state.Project.IsNull() {
		// The global definition cannot be unset; restore SonarQube's default.
		_, err = r.c().NewCodePeriods.Set(ctx, &sonarqube.NewCodePeriodsSetOptions{Type: "PREVIOUS_VERSION"})
	} else {
		_, err = r.c().NewCodePeriods.Unset(ctx, &sonarqube.NewCodePeriodsUnsetOptions{Project: state.Project.ValueString(), Branch: state.Branch.ValueString()})
	}
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to reset the new code definition", err)
	}
}

func (r *newCodePeriodsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	m := newCodePeriodsModel{ID: types.StringValue(req.ID), Project: types.StringNull(), Branch: types.StringNull(), Type: types.StringNull(), Value: types.StringNull()}
	if req.ID != "global" {
		project, branch, _ := strings.Cut(req.ID, "/")
		m.Project, m.Branch = types.StringValue(project), strOrNull(branch)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_project_links

type projectLinkResource struct{ resourceBase }

func newProjectLinkResource() resource.Resource { return &projectLinkResource{} }

type projectLinkModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectKey types.String `tfsdk:"project_key"`
	Name       types.String `tfsdk:"name"`
	URL        types.String `tfsdk:"url"`
	Type       types.String `tfsdk:"type"`
}

func (r *projectLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_links"
}

func (r *projectLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A custom link shown on a project. SonarQube cannot edit links, so changes replace it. Import ID: `project_key/id`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_key": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project key."},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Link name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 128)}},
			"url": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Link URL.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 2048)}},
			"type": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "Link type reported by SonarQube."},
		},
	}
}

func (r *projectLinkResource) find(ctx context.Context, project, id string) (*sonarqube.ProjectLinksSearchResponseLink, error) {
	res, _, err := r.c().ProjectLinks.Search(ctx, &sonarqube.ProjectLinksSearchOptions{ProjectKey: project})
	if err != nil {
		return nil, err
	}
	for _, l := range res.Links {
		if string(l.ID) == id {
			return l, nil
		}
	}
	return nil, nil
}

func (r *projectLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().ProjectLinks.Create(ctx, &sonarqube.ProjectLinksCreateOptions{ProjectKey: plan.ProjectKey.ValueString(), Name: plan.Name.ValueString(), URL: plan.URL.ValueString()})
	if err != nil {
		createError(&resp.Diagnostics, "project link", "", err)
		return
	}
	plan.ID = types.StringValue(string(res.Link.ID))
	plan.Type = types.StringValue("custom")
	if l, err := r.find(ctx, plan.ProjectKey.ValueString(), plan.ID.ValueString()); err == nil && l != nil {
		plan.Type = types.StringValue(l.Type)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.find(ctx, state.ProjectKey.ValueString(), state.ID.ValueString())
	if isGone(err) || (err == nil && l == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read project links", err)
		return
	}
	state.Name, state.URL, state.Type = types.StringValue(l.Name), types.StringValue(l.URL), types.StringValue(l.Type)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectLinkResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *projectLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c().ProjectLinks.Delete(ctx, &sonarqube.ProjectLinksDeleteOptions{ID: state.ID.ValueString()}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete project link", err)
	}
}

func (r *projectLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, "/", 2, "project_key/id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_key"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

// ---------------------------------------------------------------------------
// sonarqube_project_main_branch

type projectMainBranchResource struct{ resourceBase }

func newProjectMainBranchResource() resource.Resource { return &projectMainBranchResource{} }

type projectMainBranchModel struct {
	ID      types.String `tfsdk:"id"`
	Project types.String `tfsdk:"project"`
	Name    types.String `tfsdk:"name"`
}

func (r *projectMainBranchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_main_branch"
}

func (r *projectMainBranchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "The name of a project's main branch. If a branch with that name exists, it becomes the main branch; " +
			"otherwise the main branch is renamed. Destroying it leaves the branch as it is. Import ID: `project/name`.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state."},
			"project": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Project key."},
			"name": schema.StringAttribute{Required: true, Description: "Main branch name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
		},
	}
}

func (r *projectMainBranchResource) apply(ctx context.Context, m *projectMainBranchModel) error {
	project, name := m.Project.ValueString(), m.Name.ValueString()
	branches, _, err := r.c().ProjectBranches.List(ctx, &sonarqube.ProjectBranchesListOptions{Project: project})
	if err != nil {
		return err
	}
	for _, b := range branches.Branches {
		if b.Name == name {
			if b.IsMain {
				return nil
			}
			_, err = r.c().ProjectBranches.SetMain(ctx, &sonarqube.ProjectBranchesSetMainOptions{Project: project, Branch: name})
			return err
		}
	}
	_, err = r.c().ProjectBranches.Rename(ctx, &sonarqube.ProjectBranchesRenameOptions{Project: project, Name: name})
	return err
}

func (r *projectMainBranchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectMainBranchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the main branch", err)
		return
	}
	plan.ID = types.StringValue(plan.Project.ValueString() + "/" + plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMainBranchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectMainBranchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	branches, _, err := r.c().ProjectBranches.List(ctx, &sonarqube.ProjectBranchesListOptions{Project: state.Project.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list branches", err)
		return
	}
	for _, b := range branches.Branches {
		if b.IsMain {
			state.Name = types.StringValue(b.Name)
		}
	}
	state.ID = types.StringValue(state.Project.ValueString() + "/" + state.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectMainBranchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectMainBranchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the main branch", err)
		return
	}
	plan.ID = types.StringValue(plan.Project.ValueString() + "/" + plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMainBranchResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *projectMainBranchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	i := strings.Index(req.ID, "/")
	if i <= 0 || i == len(req.ID)-1 {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected `project/name`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &projectMainBranchModel{ID: types.StringValue(req.ID),
		Project: types.StringValue(req.ID[:i]), Name: types.StringValue(req.ID[i+1:])})...)
}

// ---------------------------------------------------------------------------
// sonarqube_plugin

var _ resource.ResourceWithModifyPlan = (*pluginResource)(nil)

type pluginResource struct{ resourceBase }

func newPluginResource() resource.Resource { return &pluginResource{} }

type pluginModel struct {
	ID              types.String `tfsdk:"id"`
	Key             types.String `tfsdk:"key"`
	RestartRequired types.Bool   `tfsdk:"restart_required"`
}

func (r *pluginResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin"
}

func (r *pluginResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A plugin installed from the Marketplace, which only Community Build has: commercial editions bundle " +
			"their plugins, and further ones are installed by hand (copied to `extensions/plugins`, then a restart). " +
			"Installation and removal take effect when the server restarts; until then the plugin is pending, and " +
			"`restart_required` is true. Installing needs `sonar.plugins.risk.consent` set to `ACCEPTED`. On a commercial " +
			"edition, e.g. with state migrated from another provider, destroying the resource only removes it from the " +
			"state, with a warning: delete the jar by hand to uninstall the plugin.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key":              schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Plugin key."},
			"restart_required": schema.BoolAttribute{Computed: true, Description: "Whether a server restart is pending for this plugin."},
		},
	}
}

// ModifyPlan rejects a new plugin on a commercial edition, which has no
// Marketplace to install it from.
func (r *pluginResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || !req.State.Raw.IsNull() || !r.configured() {
		return
	}
	info, err := r.p.info.get(ctx)
	if err != nil {
		resp.Diagnostics.AddWarning("Could not verify SonarQube edition", err.Error())
		return
	}
	if info.hasEdition("developer") {
		resp.Diagnostics.AddError("The Marketplace is not available on this server",
			fmt.Sprintf("SonarQube %s edition has no Marketplace: its plugins are bundled, and further ones are installed by hand, "+
				"by copying the plugin's jar to extensions/plugins and restarting the server. sonarqube_plugin only works with Community Build.", info.Edition))
	}
}

// pendingPlugins lists the plugin operations that wait for a restart.
type pendingPlugins struct {
	Installing, Updating, Removing []struct{ Key string }
}

func (p *pendingPlugins) installing(key string) bool {
	for _, x := range p.Installing {
		if x.Key == key {
			return true
		}
	}
	return false
}

func (r *pluginResource) pending(ctx context.Context) (*pendingPlugins, error) {
	_, raw, err := r.c().Plugins.Pending(ctx)
	if err != nil {
		return nil, err
	}
	var pend pendingPlugins
	if err := json.Unmarshal(raw.Body, &pend); err != nil {
		return nil, err
	}
	return &pend, nil
}

// status reports whether the plugin is installed (or pending installation),
// and whether a restart is pending for it.
func (r *pluginResource) status(ctx context.Context, key string) (present, pending bool, err error) {
	inst, _, err := r.c().Plugins.Installed(ctx, &sonarqube.PluginsInstalledOptions{})
	if err != nil {
		return false, false, err
	}
	for _, p := range inst.Plugins {
		present = present || p.Key == key
	}
	pend, err := r.pending(ctx)
	if err != nil {
		return false, false, err
	}
	for _, p := range pend.Installing {
		if p.Key == key {
			present, pending = true, true
		}
	}
	for _, p := range pend.Updating {
		pending = pending || p.Key == key
	}
	for _, p := range pend.Removing {
		if p.Key == key {
			present, pending = false, true
		}
	}
	return present, pending, nil
}

func (r *pluginResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pluginModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := plan.Key.ValueString()
	unlock := r.p.locks.lock("plugins")
	defer unlock()
	present, _, err := r.status(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read plugins", err)
		return
	}
	if !present {
		if _, err := r.c().Plugins.Install(ctx, &sonarqube.PluginsInstallOptions{Key: key}); err != nil {
			detail := explain(err)
			// SonarQube: "Can't install plugin without accepting firstly plugins risk consent".
			if hasMessage(err, "risk consent") {
				detail += "\n\nAccept the risk of third-party plugins first: set sonar.plugins.risk.consent to ACCEPTED, " +
					"e.g. with a sonarqube_setting this resource depends on."
			}
			resp.Diagnostics.AddError("Unable to install plugin "+key, detail)
			return
		}
	}
	_, pending, _ := r.status(ctx, key)
	plan.ID = plan.Key
	plan.RestartRequired = types.BoolValue(pending)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pluginResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pluginModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.Key.ValueString()
	if key == "" {
		key = state.ID.ValueString()
	}
	present, pending, err := r.status(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read plugins", err)
		return
	}
	if !present {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID, state.Key = types.StringValue(key), types.StringValue(key)
	state.RestartRequired = types.BoolValue(pending)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pluginResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {}

func (r *pluginResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pluginModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.Key.ValueString()
	// Commercial editions have no Marketplace: their plugins are removed by
	// hand, so destroying only forgets the plugin. When the edition cannot be
	// detected, uninstalling is attempted as on Community Build.
	if info, err := r.p.info.get(ctx); err == nil && info.hasEdition("developer") {
		resp.Diagnostics.AddWarning("Plugin "+key+" left installed",
			fmt.Sprintf("SonarQube %s edition has no Marketplace, so plugins cannot be uninstalled through the API. "+
				"sonarqube_plugin.%s was removed from the Terraform state only: to uninstall the plugin, delete its jar from "+
				"extensions/plugins (on every application node of a Data Center) and restart the server.", info.Edition, key))
		return
	}
	unlock := r.p.locks.lock("plugins")
	defer unlock()
	pend, err := r.pending(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read pending plugins", err)
		return
	}
	if pend.installing(key) {
		// Not installed yet, so it cannot be uninstalled: cancel the install.
		if err := r.cancelInstall(ctx, key, pend); err != nil {
			apiError(&resp.Diagnostics, "Unable to cancel the installation of plugin "+key, err)
		}
		return
	}
	if _, err := r.c().Plugins.Uninstall(ctx, &sonarqube.PluginsUninstallOptions{Key: key}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to uninstall plugin "+key, err)
	}
}

// cancelInstall cancels the pending installation of key. The API can only
// cancel every pending operation at once, so the others are requested again.
func (r *pluginResource) cancelInstall(ctx context.Context, key string, pend *pendingPlugins) error {
	if _, err := r.c().Plugins.CancelAll(ctx); err != nil {
		return err
	}
	var errs []error
	for _, p := range pend.Installing {
		if p.Key != key {
			_, err := r.c().Plugins.Install(ctx, &sonarqube.PluginsInstallOptions{Key: p.Key})
			errs = append(errs, err)
		}
	}
	for _, p := range pend.Updating {
		_, err := r.c().Plugins.Update(ctx, &sonarqube.PluginsUpdateOptions{Key: p.Key})
		errs = append(errs, err)
	}
	for _, p := range pend.Removing {
		_, err := r.c().Plugins.Uninstall(ctx, &sonarqube.PluginsUninstallOptions{Key: p.Key})
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("cancelled all pending plugin operations, but could not restore the others: %w", err)
	}
	return nil
}

func (r *pluginResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), req.ID)...)
}

func (r *webhookResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *newCodePeriodsResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *projectLinkResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *projectMainBranchResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *pluginResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
