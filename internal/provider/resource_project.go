package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	"golang.org/x/sync/errgroup"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

var (
	_ resource.ResourceWithImportState    = (*projectResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*projectResource)(nil)
	_ resource.ResourceWithValidateConfig = (*projectResource)(nil)
)

type projectResource struct{ resourceBase }

func newProjectResource() resource.Resource { return &projectResource{} }

type projectModel struct {
	ID                 types.String   `tfsdk:"id"`
	Name               types.String   `tfsdk:"name"`
	Project            types.String   `tfsdk:"project"`
	Visibility         types.String   `tfsdk:"visibility"`
	Tags               types.Set      `tfsdk:"tags"`
	RetrieveBadgeToken types.Bool     `tfsdk:"retrieve_badge_token"`
	BadgeToken         types.String   `tfsdk:"badge_token"`
	ContainsAICode     types.Bool     `tfsdk:"contains_ai_code"`
	Setting            []settingModel `tfsdk:"setting"`
}

var tagRe = regexp.MustCompile(`^[a-z0-9+#\-.]+$`)

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A SonarQube project.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The project key.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Display name. SonarQube has no API to rename a project, so changing it is refused at plan time instead of " +
					"destroying the project and its analysis history. If analyses set the name through `sonar.projectName`, add " +
					"`lifecycle { ignore_changes = [name] }`; to recreate the project deliberately, use `terraform apply -replace`.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 500), noSurroundingSpace{}},
			},
			"project": schema.StringAttribute{
				Required:    true,
				Description: "Project key. Changing it renames the key in place and keeps the project's history.",
				Validators:  []validator.String{projectKeyValidator{}},
			},
			"visibility": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "`public` or `private`. When omitted, the server's default project visibility applies " +
					"(the previous provider forced `public`, which could expose code on private-by-default servers).",
				Validators:    []validator.String{stringvalidator.OneOf("public", "private")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tags": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Description: "Project tags. When omitted, tags are not managed; set `[]` to remove all tags.",
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.RegexMatches(tagRe,
					"tags must be lowercase and contain only a-z, 0-9, '+', '#', '-' and '.'"))},
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"retrieve_badge_token": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Read the project's badge token into `badge_token`. Set to `false` to keep it out of state.",
			},
			"badge_token": schema.StringAttribute{Computed: true, Description: "Badge token of the project."},
			"contains_ai_code": schema.BoolAttribute{Optional: true,
				Description: "Whether the project contains AI-generated code, which lets AI Code Assurance quality gates apply " +
					"to it. Requires the Developer edition or above. When omitted, the flag is not managed.",
			},
		},
		Blocks: map[string]schema.Block{"setting": settingBlock()},
	}
}

func (r *projectResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var blocks []settingModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("setting"), &blocks)...)
	validateSettingBlocks(ctx, blocks, &resp.Diagnostics)
}

func (r *projectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var ai types.Bool
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("contains_ai_code"), &ai)...)
	if !ai.IsNull() && r.configured() {
		r.p.requireEdition(ctx, &resp.Diagnostics, "contains_ai_code", "developer")
	}
	if req.State.Raw.IsNull() {
		return
	}
	var state, plan projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Name.IsUnknown() && plan.Name.ValueString() != state.Name.ValueString() {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Project name cannot be changed",
			fmt.Sprintf("Project %q is named %q in SonarQube, but the configuration says %q. SonarQube has no API to rename a project, "+
				"and recreating it would delete its analysis history.\n\nEither set name to %q, add `lifecycle { ignore_changes = [name] }` "+
				"if analyses manage the name, or recreate the project deliberately with `terraform apply -replace=<address>`.",
				state.Project.ValueString(), state.Name.ValueString(), plan.Name.ValueString(), state.Name.ValueString()))
	}
	if !plan.Project.Equal(state.Project) {
		resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())
	}
	if !plan.RetrieveBadgeToken.Equal(state.RetrieveBadgeToken) {
		resp.Plan.SetAttribute(ctx, path.Root("badge_token"), types.StringUnknown())
	} else {
		resp.Plan.SetAttribute(ctx, path.Root("badge_token"), state.BadgeToken)
	}
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := plan.Project.ValueString()
	_, _, err := r.c().Projects.Create(ctx, &sonarqube.ProjectsCreateOptions{
		Project:    key,
		Name:       plan.Name.ValueString(),
		Visibility: plan.Visibility.ValueString(), // "" when unknown: server default
	})
	if err != nil {
		createError(&resp.Diagnostics, "project", key, err)
		return
	}
	// Record the project at once: if a later step fails, Terraform keeps it
	// as tainted instead of losing track of an object that now exists.
	state := plan
	state.ID = types.StringValue(key)
	state.BadgeToken = types.StringNull()
	if state.Visibility.IsUnknown() {
		state.Visibility = types.StringNull()
	}
	if state.Tags.IsUnknown() {
		state.Tags = types.SetNull(types.StringType)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if !plan.Tags.IsUnknown() && !plan.Tags.IsNull() {
		if err := setProjectTags(ctx, r.c(), key, stringsOf(ctx, plan.Tags, &resp.Diagnostics)); err != nil {
			apiError(&resp.Diagnostics, "Unable to set tags of project "+key, err)
			return
		}
	}
	if len(plan.Setting) > 0 {
		syncComponentSettings(ctx, r.p, key, plan.Setting, nil, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !plan.ContainsAICode.IsNull() && !plan.ContainsAICode.IsUnknown() {
		if err := setContainsAICode(ctx, r.c(), key, plan.ContainsAICode.ValueBool()); err != nil {
			apiError(&resp.Diagnostics, "Unable to set contains_ai_code of project "+key, err)
			return
		}
	}
	r.readInto(ctx, &state, &resp.Diagnostics, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// projectView is api/components/show plus the fields the typed client model
// omits.
type projectView struct {
	Component struct {
		Key        string   `json:"key"`
		Name       string   `json:"name"`
		Qualifier  string   `json:"qualifier"`
		Visibility string   `json:"visibility"`
		Tags       []string `json:"tags"`
	} `json:"component"`
}

func showComponent(ctx context.Context, c *sonarqube.Client, key string) (*projectView, error) {
	_, raw, err := c.Components.Show(ctx, &sonarqube.ComponentsShowOptions{Component: key})
	if err != nil {
		return nil, err
	}
	var v projectView
	if err := json.Unmarshal(raw.Body, &v); err != nil {
		return nil, fmt.Errorf("decoding api/components/show: %w", err)
	}
	return &v, nil
}

// readInto refreshes m from the server. It returns false when the project no
// longer exists. The component, its badge token and its settings are read
// concurrently. afterWrite tolerates a short read-after-write delay.
func (r *projectResource) readInto(ctx context.Context, m *projectModel, diags *diag.Diagnostics, afterWrite bool) bool {
	key := m.ID.ValueString()
	var (
		view     *projectView
		token    string
		settings []apiSetting
		mu       sync.Mutex
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		if afterWrite {
			err = waitFor(gctx, consistencyWindow, func(ctx context.Context) (bool, error) {
				v, err := showComponent(ctx, r.c(), key)
				if isGone(err) {
					return false, nil
				}
				mu.Lock()
				view = v
				mu.Unlock()
				return err == nil, err
			})
		} else {
			view, err = showComponent(gctx, r.c(), key)
		}
		return err
	})
	if m.RetrieveBadgeToken.IsNull() || m.RetrieveBadgeToken.ValueBool() {
		g.Go(func() error {
			t, _, err := r.c().ProjectBadges.Token(gctx, &sonarqube.ProjectBadgesTokenOptions{Project: key})
			if err != nil {
				if sonarqube.StatusCode(err) == 400 || isGone(err) {
					return nil // project missing: reported by components/show
				}
				return fmt.Errorf("reading badge token: %w", err)
			}
			token = t.Token
			return nil
		})
	}
	var ai *bool
	if !m.ContainsAICode.IsNull() {
		g.Go(func() error {
			res, _, err := r.c().Projects.GetContainsAiCode(gctx, &sonarqube.ProjectsGetContainsAiCodeOptions{Project: key})
			if isGone(err) {
				return nil // project missing: reported by components/show
			}
			if err != nil {
				return fmt.Errorf("reading contains_ai_code: %w", err)
			}
			ai = &res.ContainsAICode
			return nil
		})
	}
	if len(m.Setting) > 0 {
		g.Go(func() error {
			s, err := readComponentSettings(gctx, r.c(), key, settingKeys(m.Setting))
			if isGone(err) {
				return nil
			}
			settings = s
			return err
		})
	}
	if err := g.Wait(); err != nil {
		if isGone(err) {
			return false
		}
		apiError(diags, "Unable to read project "+key, err)
		return true
	}
	if view == nil {
		return false
	}
	m.ID = types.StringValue(view.Component.Key)
	m.Project = types.StringValue(view.Component.Key)
	m.Name = types.StringValue(view.Component.Name)
	m.Visibility = types.StringValue(view.Component.Visibility)
	m.Tags = stringSet(view.Component.Tags)
	if m.RetrieveBadgeToken.IsNull() {
		m.RetrieveBadgeToken = types.BoolValue(true)
	}
	if m.RetrieveBadgeToken.ValueBool() {
		m.BadgeToken = types.StringValue(token)
	} else {
		m.BadgeToken = types.StringNull()
	}
	if settings != nil {
		m.Setting = settingsState(ctx, m.Setting, settings, diags)
	}
	if ai != nil {
		m.ContainsAICode = types.BoolValue(*ai)
	}
	return true
}

func setContainsAICode(ctx context.Context, c *sonarqube.Client, key string, v bool) error {
	_, err := c.Projects.SetContainsAiCode(ctx, &sonarqube.ProjectsSetContainsAiCodeOptions{Project: key, ContainsAICode: strconv.FormatBool(v)})
	return err
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.readInto(ctx, &state, &resp.Diagnostics, false) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := state.ID.ValueString()

	// The key must change first: every later call addresses the new key.
	if newKey := plan.Project.ValueString(); newKey != key {
		if _, err := r.c().Projects.UpdateKey(ctx, &sonarqube.ProjectsUpdateKeyOptions{From: key, To: newKey}); err != nil {
			apiError(&resp.Diagnostics, fmt.Sprintf("Unable to change project key %s to %s", key, newKey), err)
			return
		}
		key = newKey
		state.ID, state.Project = types.StringValue(key), types.StringValue(key)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
	if !plan.Visibility.IsUnknown() && !plan.Visibility.Equal(state.Visibility) {
		_, err := r.c().Projects.UpdateVisibility(ctx, &sonarqube.ProjectsUpdateVisibilityOptions{Project: key, Visibility: plan.Visibility.ValueString()})
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to change visibility of project "+key, err)
			return
		}
	}
	if !plan.Tags.IsUnknown() && !plan.Tags.IsNull() && !plan.Tags.Equal(state.Tags) {
		if err := setProjectTags(ctx, r.c(), key, stringsOf(ctx, plan.Tags, &resp.Diagnostics)); err != nil {
			apiError(&resp.Diagnostics, "Unable to set tags of project "+key, err)
			return
		}
	}
	if len(plan.Setting) > 0 {
		syncComponentSettings(ctx, r.p, key, plan.Setting, settingKeys(state.Setting), &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !plan.ContainsAICode.IsNull() && !plan.ContainsAICode.IsUnknown() && !plan.ContainsAICode.Equal(state.ContainsAICode) {
		if err := setContainsAICode(ctx, r.c(), key, plan.ContainsAICode.ValueBool()); err != nil {
			apiError(&resp.Diagnostics, "Unable to set contains_ai_code of project "+key, err)
			return
		}
	}
	plan.ID = types.StringValue(key)
	if !r.readInto(ctx, &plan, &resp.Diagnostics, true) {
		resp.Diagnostics.AddError("Project disappeared", fmt.Sprintf("Project %s was not found after update.", key))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().Projects.Delete(ctx, &sonarqube.ProjectsDeleteOptions{Project: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete project "+state.ID.ValueString(), err)
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	const metadataOnly = "?retrieve_badge_token=false"
	id := strings.TrimSuffix(req.ID, metadataOnly)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("retrieve_badge_token"), !strings.HasSuffix(req.ID, metadataOnly))...)
}

// setProjectTags replaces the project's tags. The typed client omits an
// empty tag list, so clearing goes through a raw call with tags="".
func setProjectTags(ctx context.Context, c *sonarqube.Client, key string, tags []string) error {
	if len(tags) == 0 {
		return rawPost(ctx, c, "/api/project_tags/set", url.Values{"project": {key}, "tags": {""}})
	}
	_, err := c.ProjectTags.Set(ctx, &sonarqube.ProjectTagsSetOptions{Project: key, Tags: tags})
	return err
}

func (r *projectResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
