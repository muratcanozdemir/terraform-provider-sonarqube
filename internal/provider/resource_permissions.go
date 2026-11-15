package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

// searchQ returns q for permission searches, which reject queries shorter
// than three characters; for those, the caller pages through everything
// and matches exactly.
func searchQ(name string) string {
	if len(name) < 3 || strings.EqualFold(name, "anyone") {
		return ""
	}
	return name
}

// ---------------------------------------------------------------------------
// sonarqube_permissions

var _ resource.ResourceWithValidateConfig = (*permissionsResource)(nil)

type permissionsResource struct{ resourceBase }

func newPermissionsResource() resource.Resource { return &permissionsResource{} }

type permissionsModel struct {
	ID               types.String `tfsdk:"id"`
	LoginName        types.String `tfsdk:"login_name"`
	GroupName        types.String `tfsdk:"group_name"`
	SpecialGroupName types.String `tfsdk:"special_group_name"`
	ProjectKey       types.String `tfsdk:"project_key"`
	TemplateID       types.String `tfsdk:"template_id"`
	TemplateName     types.String `tfsdk:"template_name"`
	Permissions      types.Set    `tfsdk:"permissions"`
}

func (r *permissionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions"
}

func (r *permissionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	principal := stringvalidator.ExactlyOneOf(path.MatchRoot("login_name"), path.MatchRoot("group_name"), path.MatchRoot("special_group_name"))
	scope := stringvalidator.ConflictsWith(path.MatchRoot("project_key"), path.MatchRoot("template_id"), path.MatchRoot("template_name"))
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "The complete set of permissions of one user or group, globally, on a project, or in a permission template. " +
			"Permissions the principal has in that scope but that are not listed are removed. " +
			"Import ID: `principal` (global) or `principal:p_<project>`, `principal:t_<template id>`, `principal:tn_<template name>`.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"login_name": schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{principal}, Description: "User login."},
			"group_name": schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{principal}, Description: "Group name, or `anyone`."},
			"special_group_name": schema.StringAttribute{Optional: true, PlanModifiers: replace,
				Validators:  []validator.String{principal, stringvalidator.OneOf("project_creator")},
				Description: "`project_creator`: the creator of a project, in a permission template."},
			"project_key":   schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{scope}, Description: "Project scope."},
			"template_id":   schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{scope}, Description: "Permission template scope, by ID."},
			"template_name": schema.StringAttribute{Optional: true, PlanModifiers: replace, Validators: []validator.String{scope}, Description: "Permission template scope, by name."},
			"permissions": schema.SetAttribute{Required: true, ElementType: types.StringType,
				Validators: []validator.Set{setvalidator.SizeAtLeast(1)},
				Description: "Permissions, validated by the server since they vary by version and edition. Global: `admin`, `gateadmin`, `profileadmin`, " +
					"`provisioning`, `scan`, `applicationcreator`, `portfoliocreator`. Project and template: `admin`, `codeviewer`, `issueadmin`, " +
					"`securityhotspotadmin`, `scan`, `user`."},
		},
	}
}

func (r *permissionsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg permissionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if !cfg.SpecialGroupName.IsNull() && cfg.TemplateID.IsNull() && cfg.TemplateName.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("special_group_name"), "Template required",
			"project_creator permissions exist only in permission templates; set template_id or template_name.")
	}
}

func (m *permissionsModel) scope() string {
	switch {
	case !m.ProjectKey.IsNull():
		return "p_" + m.ProjectKey.ValueString()
	case !m.TemplateID.IsNull():
		return "t_" + m.TemplateID.ValueString()
	case !m.TemplateName.IsNull():
		return "tn_" + m.TemplateName.ValueString()
	}
	return "global"
}

func (m *permissionsModel) id() types.String {
	switch {
	case !m.LoginName.IsNull():
		return types.StringValue(fmt.Sprintf("user-%s-%s-permissions", m.LoginName.ValueString(), m.scope()))
	case !m.GroupName.IsNull():
		return types.StringValue(fmt.Sprintf("group-%s-%s-permissions", m.GroupName.ValueString(), m.scope()))
	}
	return types.StringValue(fmt.Sprintf("project-creator-%s-permissions", m.scope()))
}

func (m *permissionsModel) template() bool { return !m.TemplateID.IsNull() || !m.TemplateName.IsNull() }

// change grants (add) or revokes (!add) one permission.
func (r *permissionsResource) change(ctx context.Context, m *permissionsModel, perm string, add bool) error {
	c := r.c()
	login, group := m.LoginName.ValueString(), m.GroupName.ValueString()
	tid, tname, project := m.TemplateID.ValueString(), m.TemplateName.ValueString(), m.ProjectKey.ValueString()
	var err error
	switch {
	case !m.SpecialGroupName.IsNull() && add:
		_, err = c.Permissions.AddProjectCreatorToTemplate(ctx, &sonarqube.PermissionsAddProjectCreatorToTemplateOptions{Permission: perm, TemplateID: tid, TemplateName: tname})
	case !m.SpecialGroupName.IsNull():
		_, err = c.Permissions.RemoveProjectCreatorFromTemplate(ctx, &sonarqube.PermissionsRemoveProjectCreatorFromTemplateOptions{Permission: perm, TemplateID: tid, TemplateName: tname})
	case m.template() && login != "" && add:
		_, err = c.Permissions.AddUserToTemplate(ctx, &sonarqube.PermissionsAddUserToTemplateOptions{Login: login, Permission: perm, TemplateID: tid, TemplateName: tname})
	case m.template() && login != "":
		_, err = c.Permissions.RemoveUserFromTemplate(ctx, &sonarqube.PermissionsRemoveUserFromTemplateOptions{Login: login, Permission: perm, TemplateID: tid, TemplateName: tname})
	case m.template() && add:
		_, err = c.Permissions.AddGroupToTemplate(ctx, &sonarqube.PermissionsAddGroupToTemplateOptions{GroupName: group, Permission: perm, TemplateID: tid, TemplateName: tname})
	case m.template():
		_, err = c.Permissions.RemoveGroupFromTemplate(ctx, &sonarqube.PermissionsRemoveGroupFromTemplateOptions{GroupName: group, Permission: perm, TemplateID: tid, TemplateName: tname})
	case login != "" && add:
		_, err = c.Permissions.AddUser(ctx, &sonarqube.PermissionsAddUserOptions{Login: login, Permission: perm, ProjectKey: project})
	case login != "":
		_, err = c.Permissions.RemoveUser(ctx, &sonarqube.PermissionsRemoveUserOptions{Login: login, Permission: perm, ProjectKey: project})
	case add:
		_, err = c.Permissions.AddGroup(ctx, &sonarqube.PermissionsAddGroupOptions{GroupName: group, Permission: perm, ProjectKey: project})
	default:
		_, err = c.Permissions.RemoveGroup(ctx, &sonarqube.PermissionsRemoveGroupOptions{GroupName: group, Permission: perm, ProjectKey: project})
	}
	return err
}

type principalPerms struct {
	Login       string   `json:"login"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// current returns the principal's permissions in the scope. The typed client
// models group permissions as raw JSON, so all listings are decoded here.
func (r *permissionsResource) current(ctx context.Context, m *permissionsModel) ([]string, error) {
	c := r.c()
	if !m.SpecialGroupName.IsNull() {
		t, err := findTemplate(ctx, c, m.TemplateID.ValueString(), m.TemplateName.ValueString())
		if err != nil || t == nil {
			return nil, err
		}
		var out []string
		for _, p := range t.Permissions {
			if p.WithProjectCreator {
				out = append(out, p.Key)
			}
		}
		return out, nil
	}
	isUser := !m.LoginName.IsNull()
	name := m.GroupName.ValueString()
	q := url.Values{}
	var endpoint, listKey string
	switch {
	case m.template() && isUser:
		endpoint, listKey = "/api/permissions/template_users", "users"
	case m.template():
		endpoint, listKey = "/api/permissions/template_groups", "groups"
	case isUser:
		endpoint, listKey = "/api/permissions/users", "users"
	default:
		endpoint, listKey = "/api/permissions/groups", "groups"
	}
	if isUser {
		name = m.LoginName.ValueString()
	}
	if m.template() {
		if v := m.TemplateID.ValueString(); v != "" {
			q.Set("templateId", v)
		} else {
			q.Set("templateName", m.TemplateName.ValueString())
		}
	} else if v := m.ProjectKey.ValueString(); v != "" {
		q.Set("projectKey", v)
	}
	if s := searchQ(name); s != "" {
		q.Set("q", s)
	}
	items, err := collect(ctx, 100, func(ctx context.Context, page, size int) ([]principalPerms, int64, error) {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("p", fmt.Sprint(page))
		pq.Set("ps", fmt.Sprint(size))
		var raw json.RawMessage
		if _, err := c.Do(ctx, &sonarqube.Request{Method: "GET", Path: endpoint, Query: pq}, &raw); err != nil {
			return nil, 0, err
		}
		var res map[string]json.RawMessage
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, 0, err
		}
		var list []principalPerms
		var pg sonarqube.Paging
		_ = json.Unmarshal(res[listKey], &list)
		_ = json.Unmarshal(res["paging"], &pg)
		return list, pg.Total, nil
	})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if (isUser && it.Login == name) || (!isUser && strings.EqualFold(it.Name, name)) {
			return it.Permissions, nil
		}
	}
	return nil, nil
}

// implicitPerms are held by everyone on a public project. SonarQube accepts
// but ignores grants of them there, so they are treated as already held.
var implicitPerms = []string{"user", "codeviewer"}

func (r *permissionsResource) publicProject(ctx context.Context, m *permissionsModel) (bool, error) {
	if m.ProjectKey.IsNull() {
		return false, nil
	}
	v, err := showComponent(ctx, r.c(), m.ProjectKey.ValueString())
	if err != nil {
		return false, err
	}
	return v.Component.Visibility == "public", nil
}

// effective is current() plus the implicit permissions of a public project
// that the caller wants.
func (r *permissionsResource) effective(ctx context.Context, m *permissionsModel, want []string) ([]string, error) {
	have, err := r.current(ctx, m)
	if err != nil {
		return nil, err
	}
	public, err := r.publicProject(ctx, m)
	if err != nil {
		return nil, err
	}
	if public {
		for _, p := range implicitPerms {
			if slices.Contains(want, p) && !slices.Contains(have, p) {
				have = append(have, p)
			}
		}
	}
	return have, nil
}

func (r *permissionsResource) apply(ctx context.Context, m *permissionsModel, add, remove []string, diags *diag.Diagnostics) {
	type op struct {
		perm string
		add  bool
	}
	var ops []op
	for _, p := range add {
		ops = append(ops, op{p, true})
	}
	for _, p := range remove {
		ops = append(ops, op{p, false})
	}
	err := forEach(ctx, ops, func(ctx context.Context, o op) error {
		if err := r.change(ctx, m, o.perm, o.add); err != nil {
			verb := "grant"
			if !o.add {
				verb = "revoke"
			}
			return fmt.Errorf("%s %s: %w", verb, o.perm, err)
		}
		return nil
	})
	if err != nil {
		apiError(diags, "Unable to change permissions", err)
	}
}

func (r *permissionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan permissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want := stringsOf(ctx, plan.Permissions, &resp.Diagnostics)
	have, err := r.effective(ctx, &plan, want)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read current permissions", err)
		return
	}
	plan.ID = plan.id()
	add, remove := diffStrings(have, want)
	r.apply(ctx, &plan, add, remove, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *permissionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state permissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	have, err := r.effective(ctx, &state, stringsOf(ctx, state.Permissions, &resp.Diagnostics))
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read permissions", err)
		return
	}
	state.ID = state.id()
	state.Permissions = stringSet(have)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *permissionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan permissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want := stringsOf(ctx, plan.Permissions, &resp.Diagnostics)
	have, err := r.effective(ctx, &plan, want) // live values, not state: robust to drift
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read current permissions", err)
		return
	}
	add, remove := diffStrings(have, want)
	r.apply(ctx, &plan, add, remove, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = plan.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *permissionsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state permissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	have, err := r.current(ctx, &state)
	if isGone(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read current permissions", err)
		return
	}
	r.apply(ctx, &state, nil, have, &resp.Diagnostics)
}

func (r *permissionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	principal, scope, _ := strings.Cut(req.ID, ":")
	if principal == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Expected `principal` or `principal:p_<project>|t_<template id>|tn_<template name>`.")
		return
	}
	m := permissionsModel{LoginName: types.StringNull(), GroupName: types.StringNull(), SpecialGroupName: types.StringNull(),
		ProjectKey: types.StringNull(), TemplateID: types.StringNull(), TemplateName: types.StringNull(), Permissions: types.SetNull(types.StringType)}
	switch {
	case scope == "":
	case strings.HasPrefix(scope, "p_"):
		m.ProjectKey = types.StringValue(scope[2:])
	case strings.HasPrefix(scope, "tn_"):
		m.TemplateName = types.StringValue(scope[3:])
	case strings.HasPrefix(scope, "t_"):
		m.TemplateID = types.StringValue(scope[2:])
	default:
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Unknown scope %q: use p_<project>, t_<template id> or tn_<template name>.", scope))
		return
	}
	switch {
	case strings.EqualFold(principal, "project_creator"):
		m.SpecialGroupName = types.StringValue("project_creator")
	default:
		u, err := findUser(ctx, r.c(), "", principal)
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to look up "+principal, err)
			return
		}
		if u != nil {
			m.LoginName = types.StringValue(u.Login)
		} else {
			m.GroupName = types.StringValue(principal)
		}
	}
	m.ID = m.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// Permission templates

func listTemplates(ctx context.Context, c *sonarqube.Client, q string) (*sonarqube.PermissionsSearchTemplatesResponse, error) {
	res, _, err := c.Permissions.SearchTemplates(ctx, &sonarqube.PermissionsSearchTemplatesOptions{Q: q})
	return res, err
}

// findTemplate returns a template by ID, or by exact name when id is "".
func findTemplate(ctx context.Context, c *sonarqube.Client, id, name string) (*sonarqube.PermissionsSearchTemplatesResponsePermissionTemplate, error) {
	res, err := listTemplates(ctx, c, name)
	if err != nil {
		return nil, err
	}
	for _, t := range res.PermissionTemplates {
		if (id != "" && string(t.ID) == id) || (id == "" && t.Name == name) {
			return t, nil
		}
	}
	return nil, nil
}

// builtInTemplate returns the ID of the template SonarQube ships with
// ("Default template", else the oldest); defaults revert to it.
func builtInTemplate(ctx context.Context, c *sonarqube.Client) (string, error) {
	res, err := listTemplates(ctx, c, "")
	if err != nil {
		return "", err
	}
	var oldest *sonarqube.PermissionsSearchTemplatesResponsePermissionTemplate
	for _, t := range res.PermissionTemplates {
		if t.Name == "Default template" {
			return string(t.ID), nil
		}
		if oldest == nil || t.CreatedAt < oldest.CreatedAt {
			oldest = t
		}
	}
	if oldest == nil {
		return "", fmt.Errorf("no permission template found")
	}
	return string(oldest.ID), nil
}

type permissionTemplateResource struct{ resourceBase }

func newPermissionTemplateResource() resource.Resource { return &permissionTemplateResource{} }

type permissionTemplateModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	ProjectKeyPattern types.String `tfsdk:"project_key_pattern"`
	Default           types.Bool   `tfsdk:"default"`
}

func (r *permissionTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permission_template"
}

func (r *permissionTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A permission template, applied to new projects.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, Description: "Template name. Changing it renames the template in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100), noSurroundingSpace{}}},
			"description": schema.StringAttribute{Optional: true, Description: "Description."},
			"project_key_pattern": schema.StringAttribute{Optional: true,
				Description: "Java regular expression; new projects whose key matches get this template."},
			"default": schema.BoolAttribute{Optional: true,
				Description: "Make this the default template for projects; `false` reverts to the built-in template. When omitted, " +
					"the default is not managed here, so it can be managed with `sonarqube_permission_template_default` instead."},
		},
	}
}

func (r *permissionTemplateResource) readInto(ctx context.Context, m *permissionTemplateModel) (bool, error) {
	res, err := listTemplates(ctx, r.c(), "")
	if err != nil {
		return false, err
	}
	for _, t := range res.PermissionTemplates {
		if string(t.ID) != m.ID.ValueString() {
			continue
		}
		m.Name = types.StringValue(t.Name)
		m.Description = keepNullIfEmpty(m.Description, t.Description)
		// The typed model omits projectKeyPattern; read it from the raw listing.
		pattern, _ := templatePattern(ctx, r.c(), string(t.ID))
		m.ProjectKeyPattern = keepNullIfEmpty(m.ProjectKeyPattern, pattern)
		if !m.Default.IsNull() {
			m.Default = types.BoolValue(false)
			for _, d := range res.DefaultTemplates {
				if d.Qualifier == "TRK" && string(d.TemplateID) == string(t.ID) {
					m.Default = types.BoolValue(true)
				}
			}
		}
		return true, nil
	}
	return false, nil
}

func templatePattern(ctx context.Context, c *sonarqube.Client, id string) (string, error) {
	var res struct {
		PermissionTemplates []struct {
			ID                string `json:"id"`
			ProjectKeyPattern string `json:"projectKeyPattern"`
		} `json:"permissionTemplates"`
	}
	if _, err := c.Do(ctx, &sonarqube.Request{Method: "GET", Path: "/api/permissions/search_templates"}, &res); err != nil {
		return "", err
	}
	for _, t := range res.PermissionTemplates {
		if t.ID == id {
			return t.ProjectKeyPattern, nil
		}
	}
	return "", nil
}

func (r *permissionTemplateResource) setDefault(ctx context.Context, id string) error {
	_, err := r.c().Permissions.SetDefaultTemplate(ctx, &sonarqube.PermissionsSetDefaultTemplateOptions{TemplateID: id, Qualifier: "TRK"})
	return err
}

func (r *permissionTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan permissionTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().Permissions.CreateTemplate(ctx, &sonarqube.PermissionsCreateTemplateOptions{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), ProjectKeyPattern: plan.ProjectKeyPattern.ValueString()})
	if err != nil {
		createError(&resp.Diagnostics, "permission template", plan.Name.ValueString(), err)
		return
	}
	state := plan
	state.ID = types.StringValue(string(res.PermissionTemplate.ID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if plan.Default.ValueBool() {
		if err := r.setDefault(ctx, state.ID.ValueString()); err != nil {
			apiError(&resp.Diagnostics, "Unable to make "+plan.Name.ValueString()+" the default template", err)
			return
		}
	}
	if _, err := r.readInto(ctx, &state); err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission template", err)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *permissionTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state permissionTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.readInto(ctx, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission template", err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *permissionTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state permissionTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	// A raw call, so that an emptied description or pattern is cleared:
	// the typed options drop empty strings.
	form := url.Values{"id": {id}, "name": {plan.Name.ValueString()},
		"description": {plan.Description.ValueString()}, "projectKeyPattern": {plan.ProjectKeyPattern.ValueString()}}
	if err := rawPost(ctx, r.c(), "/api/permissions/update_template", form); err != nil {
		apiError(&resp.Diagnostics, "Unable to update permission template", err)
		return
	}
	if !plan.Default.IsNull() && !plan.Default.Equal(state.Default) {
		target := id
		if !plan.Default.ValueBool() {
			var err error
			if target, err = builtInTemplate(ctx, r.c()); err != nil {
				apiError(&resp.Diagnostics, "Unable to find the built-in permission template", err)
				return
			}
		}
		if err := r.setDefault(ctx, target); err != nil {
			apiError(&resp.Diagnostics, "Unable to change the default template", err)
			return
		}
	}
	plan.ID = state.ID
	if _, err := r.readInto(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission template", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *permissionTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state permissionTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A default template cannot be deleted: hand the default back first,
	// based on live data rather than state.
	res, err := listTemplates(ctx, r.c(), "")
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission templates", err)
		return
	}
	for _, d := range res.DefaultTemplates {
		if string(d.TemplateID) == state.ID.ValueString() {
			builtIn, err := builtInTemplate(ctx, r.c())
			if err != nil {
				apiError(&resp.Diagnostics, "Unable to find the built-in permission template", err)
				return
			}
			_, err = r.c().Permissions.SetDefaultTemplate(ctx, &sonarqube.PermissionsSetDefaultTemplateOptions{TemplateID: builtIn, Qualifier: d.Qualifier})
			if err != nil {
				apiError(&resp.Diagnostics, "Unable to move the default away from this template", err)
				return
			}
		}
	}
	_, err = r.c().Permissions.DeleteTemplate(ctx, &sonarqube.PermissionsDeleteTemplateOptions{TemplateID: state.ID.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete permission template", err)
	}
}

// ImportState accepts the template ID or its exact name.
func (r *permissionTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	t, err := findTemplate(ctx, r.c(), req.ID, "")
	if err == nil && t == nil {
		t, err = findTemplate(ctx, r.c(), "", req.ID)
	}
	if err != nil || t == nil {
		resp.Diagnostics.AddError("Permission template not found", fmt.Sprintf("No template has the ID or name %q (%v).", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), string(t.ID))...)
}

// ---------------------------------------------------------------------------
// sonarqube_permission_template_default

type permissionTemplateDefaultResource struct{ resourceBase }

func newPermissionTemplateDefaultResource() resource.Resource {
	return &permissionTemplateDefaultResource{}
}

type permissionTemplateDefaultModel struct {
	ID         types.String `tfsdk:"id"`
	TemplateID types.String `tfsdk:"template_id"`
	Qualifier  types.String `tfsdk:"qualifier"`
}

func (r *permissionTemplateDefaultResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permission_template_default"
}

func (r *permissionTemplateDefaultResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "The default permission template for one kind of component. Destroying it reverts to the built-in default template. Import ID: the qualifier.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"template_id": schema.StringAttribute{Required: true, Description: "Template ID."},
			"qualifier": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description: "`TRK` (projects), `APP` (applications) or `VW` (portfolios).",
				Validators:  []validator.String{stringvalidator.OneOf("TRK", "APP", "VW")}},
		},
	}
}

func (r *permissionTemplateDefaultResource) set(ctx context.Context, m *permissionTemplateDefaultModel) error {
	_, err := r.c().Permissions.SetDefaultTemplate(ctx, &sonarqube.PermissionsSetDefaultTemplateOptions{TemplateID: m.TemplateID.ValueString(), Qualifier: m.Qualifier.ValueString()})
	return err
}

func (r *permissionTemplateDefaultResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan permissionTemplateDefaultModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the default permission template", err)
		return
	}
	plan.ID = plan.Qualifier
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *permissionTemplateDefaultResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state permissionTemplateDefaultModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := state.Qualifier.ValueString()
	if q == "" {
		q = state.ID.ValueString()
	}
	res, err := listTemplates(ctx, r.c(), "")
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission templates", err)
		return
	}
	for _, d := range res.DefaultTemplates {
		if d.Qualifier == q {
			state.ID, state.Qualifier = types.StringValue(q), types.StringValue(q)
			state.TemplateID = types.StringValue(string(d.TemplateID))
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // qualifier not available on this edition
}

func (r *permissionTemplateDefaultResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan permissionTemplateDefaultModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to set the default permission template", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *permissionTemplateDefaultResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state permissionTemplateDefaultModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Revert only if this resource's template is still the default.
	res, err := listTemplates(ctx, r.c(), "")
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read permission templates", err)
		return
	}
	still := false
	for _, d := range res.DefaultTemplates {
		still = still || (d.Qualifier == state.Qualifier.ValueString() && string(d.TemplateID) == state.TemplateID.ValueString())
	}
	if !still {
		return
	}
	builtIn, err := builtInTemplate(ctx, r.c())
	if err == nil {
		_, err = r.c().Permissions.SetDefaultTemplate(ctx, &sonarqube.PermissionsSetDefaultTemplateOptions{TemplateID: builtIn, Qualifier: state.Qualifier.ValueString()})
	}
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to revert the default permission template", err)
	}
}

func (r *permissionTemplateDefaultResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("qualifier"), req.ID)...)
}

func (r *permissionsResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *permissionTemplateResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *permissionTemplateDefaultResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
