package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// findProfile returns the profile with the given key, or by language and
// name when key is "". It returns nil when none matches. One search call
// returns the parent and default flag too.
func findProfile(ctx context.Context, c *sonarqube.Client, key, language, name string) (*sonarqube.QualityprofilesSearchResponseProfile, error) {
	res, _, err := c.Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: language})
	if err != nil {
		return nil, err
	}
	for _, p := range res.Profiles {
		if (key != "" && p.Key == key) || (key == "" && p.Language == language && p.Name == name) {
			return p, nil
		}
	}
	return nil, nil
}

// builtInDefaultProfile returns the name of the built-in profile of a
// language, made default when a managed profile stops being the default.
func builtInDefaultProfile(ctx context.Context, c *sonarqube.Client, language string) (string, error) {
	res, _, err := c.Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: language})
	if err != nil {
		return "", err
	}
	var builtIn []string
	for _, p := range res.Profiles {
		if p.IsBuiltIn {
			builtIn = append(builtIn, p.Name)
		}
	}
	if slices.Contains(builtIn, "Sonar way") {
		return "Sonar way", nil
	}
	if len(builtIn) > 0 {
		return builtIn[0], nil
	}
	return "", fmt.Errorf("no built-in quality profile for language %s", language)
}

// ---------------------------------------------------------------------------
// sonarqube_qualityprofile

var _ resource.ResourceWithValidateConfig = (*qualityProfileResource)(nil)

type qualityProfileResource struct{ resourceBase }

func newQualityProfileResource() resource.Resource { return &qualityProfileResource{} }

type qualityProfileModel struct {
	ID        types.String `tfsdk:"id"`
	Key       types.String `tfsdk:"key"`
	Name      types.String `tfsdk:"name"`
	Language  types.String `tfsdk:"language"`
	IsDefault types.Bool   `tfsdk:"is_default"`
	Parent    types.String `tfsdk:"parent"`
	Backup    types.String `tfsdk:"backup"`
	CopyFrom  types.String `tfsdk:"copy_from"`
}

func (r *qualityProfileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofile"
}

func (r *qualityProfileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A quality profile. Destroying a profile that still has child profiles is refused, because SonarQube " +
			"would delete every descendant along with it.",
		Attributes: map[string]schema.Attribute{
			"id":  schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Profile key."},
			"key": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "Profile key."},
			"name": schema.StringAttribute{Required: true, Description: "Profile name. Changing it renames the profile in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100), noSurroundingSpace{}}},
			"language": schema.StringAttribute{Required: true, Description: "Language key, e.g. `java`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"is_default": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Make this the language's default profile. Unsetting it makes the built-in profile the default again."},
			"parent": schema.StringAttribute{Optional: true,
				Description: "Name of the parent profile to inherit rules from. Changing it re-parents the profile in place."},
			"backup": schema.StringAttribute{Optional: true,
				Description: "Rules of the profile as a backup in SonarQube's XML format, as written by *Back up* in the UI or " +
					"`api/qualityprofiles/backup` (for example `file(\"profile.xml\")`). The profile is restored from it: the " +
					"rules listed are activated with their settings and every other rule is deactivated. The backup's " +
					"`<name>` is ignored (the profile's `name` is used) and its `<language>` must match `language`. " +
					"Terraform reports drift when the profile's rules no longer match; only what the backup sets is " +
					"compared, so a hand-written backup may omit priorities, impacts or parameters. SonarQube silently " +
					"skips unknown rules on restore, so the provider fails the apply when a listed rule did not get " +
					"activated. Do not combine with `sonarqube_qualityprofile_activate_rule` on the same profile.",
				Validators: []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("parent"), path.MatchRoot("copy_from"))}},
			"copy_from": schema.StringAttribute{Optional: true,
				Description: "Name of a profile of the same language to copy when creating this one: its rules, their " +
					"settings and its parent. If the source inherits from a profile, set `parent` to that profile, or " +
					"to another one to re-parent the copy. Only used on creation; changing it to another profile " +
					"re-creates this one, and adding it to an existing (e.g. imported) profile or removing it changes nothing.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
					func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
						resp.RequiresReplace = !req.StateValue.IsNull() && !req.PlanValue.IsNull()
					}, "Copying another profile re-creates the profile.", "Copying another profile re-creates the profile.")}},
		},
	}
}

func (r *qualityProfileResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m qualityProfileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Backup.IsNull() || m.Backup.IsUnknown() {
		return
	}
	b, err := parseBackup(m.Backup.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("backup"), "Invalid quality profile backup", err.Error())
		return
	}
	if !m.Language.IsUnknown() && b.Language != m.Language.ValueString() {
		resp.Diagnostics.AddAttributeError(path.Root("backup"), "Backup of another language",
			fmt.Sprintf("The backup is for language %q, the profile for %q.", b.Language, m.Language.ValueString()))
	}
}

// exportBackup returns the profile's current backup, raw and parsed.
func (r *qualityProfileResource) exportBackup(ctx context.Context, m *qualityProfileModel) (string, *profileBackup, error) {
	raw, _, err := r.c().Qualityprofiles.Backup(ctx, &sonarqube.QualityprofilesBackupOptions{
		Language: m.Language.ValueString(), QualityProfile: m.Name.ValueString()})
	if err != nil {
		return "", nil, err
	}
	b, err := parseBackup(string(raw))
	return string(raw), b, err
}

// restore restores the configured backup into the profile named m.Name,
// creating it if needed, and checks that the server applied all of it.
func (r *qualityProfileResource) restore(ctx context.Context, m *qualityProfileModel) error {
	want, err := parseBackup(m.Backup.ValueString())
	if err != nil {
		return err
	}
	if want.Language != m.Language.ValueString() {
		return fmt.Errorf("the backup is for language %q, the profile for %q", want.Language, m.Language.ValueString())
	}
	doc, err := withName(m.Backup.ValueString(), m.Name.ValueString())
	if err != nil {
		return err
	}
	unlock := lockProfileTree(r.p)
	_, err = r.c().Qualityprofiles.Restore(ctx, &sonarqube.QualityprofilesRestoreOptions{
		Backup: &sonarqube.File{Name: "backup.xml", Content: strings.NewReader(doc)}})
	unlock()
	if err != nil {
		return err
	}
	_, got, err := r.exportBackup(ctx, m)
	if err != nil {
		return fmt.Errorf("restored, but unable to check the result: %w", err)
	}
	if missing, extra, changed := backupDiff(want, got); len(missing)+len(extra)+len(changed) > 0 {
		return fmt.Errorf("SonarQube did not apply the whole backup: %s. Rules that do not exist on the server, "+
			"belong to another language or have invalid parameters are skipped on restore", diffSummary(missing, extra, changed))
	}
	return nil
}

// copyProfile creates the profile as a copy of m.CopyFrom.
func (r *qualityProfileResource) copyProfile(ctx context.Context, m *qualityProfileModel) (string, error) {
	lang, from := m.Language.ValueString(), m.CopyFrom.ValueString()
	src, err := findProfile(ctx, r.c(), "", lang, from)
	if err != nil {
		return "", err
	}
	if src == nil {
		return "", fmt.Errorf("no %s quality profile is named %q (names are exact and case-sensitive)", lang, from)
	}
	if src.ParentName != "" && m.Parent.IsNull() {
		return "", fmt.Errorf("%q inherits from %q and a copy keeps that parent: set parent = %q, or another parent; "+
			"removing the parent would deactivate the inherited rules", from, src.ParentName, src.ParentName)
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	res, _, err := r.c().Qualityprofiles.Copy(ctx, &sonarqube.QualityprofilesCopyOptions{FromKey: src.Key, ToName: m.Name.ValueString()})
	if err != nil {
		return "", err
	}
	return res.Key, nil
}

func (r *qualityProfileResource) readInto(ctx context.Context, m *qualityProfileModel) (bool, error) {
	p, err := findProfile(ctx, r.c(), m.ID.ValueString(), m.Language.ValueString(), "")
	if err != nil || p == nil {
		return false, err
	}
	m.ID, m.Key = types.StringValue(p.Key), types.StringValue(p.Key)
	m.Name = types.StringValue(p.Name)
	m.Language = types.StringValue(p.Language)
	m.IsDefault = types.BoolValue(p.IsDefault)
	m.Parent = strOrNull(p.ParentName)
	return true, nil
}

// checkParent verifies that the parent exists with this exact name.
// api/qualityprofiles/change_parent does not: given a name that matches no
// profile it may attach a different one (e.g. "Sonar way" attaches "Sonar way
// comprehensive" on servers that no longer ship a "Sonar way" for a language).
func (r *qualityProfileResource) checkParent(ctx context.Context, m *qualityProfileModel) error {
	if m.Parent.IsNull() {
		return nil
	}
	lang, parent := m.Language.ValueString(), m.Parent.ValueString()
	res, _, err := r.c().Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: lang})
	if err != nil {
		return err
	}
	var names []string
	for _, p := range res.Profiles {
		if p.Name == parent {
			return nil
		}
		if p.Name != m.Name.ValueString() {
			names = append(names, fmt.Sprintf("%q", p.Name))
		}
	}
	return fmt.Errorf("no %s quality profile is named %q (names are exact and case-sensitive); available: %s", lang, parent, strings.Join(names, ", "))
}

// lockProfileTree serializes every change to the quality profile hierarchy
// and to rule activations within this provider instance.
//
// SonarQube cascades these changes through the hierarchy in one transaction
// each, and does not coordinate concurrent ones. Terraform runs independent
// resources in parallel, so destroying a child profile while a rule is
// deactivated on its parent deadlocks on the active_rules table: the server
// rolls one back and answers HTTP 500, leaving resources half-destroyed.
// Likewise change_parent copies the parent's rules as of its own transaction
// while activate_rule cascades only to children attached as of its own, so
// overlapping them can leave a child without the rule.
//
// Cascades reach descendants at any depth, and a profile key alone does not
// name the language, so one lock covers the whole tree.
func lockProfileTree(p *providerData) (unlock func()) {
	return p.locks.lock("qualityprofiles")
}

func (r *qualityProfileResource) setParent(ctx context.Context, m *qualityProfileModel) error {
	unlock := lockProfileTree(r.p)
	defer unlock()
	if err := r.checkParent(ctx, m); err != nil {
		return err
	}
	_, err := r.c().Qualityprofiles.ChangeParent(ctx, &sonarqube.QualityprofilesChangeParentOptions{
		Language: m.Language.ValueString(), QualityProfile: m.Name.ValueString(), ParentQualityProfile: m.Parent.ValueString()})
	return err
}

func (r *qualityProfileResource) setDefault(ctx context.Context, m *qualityProfileModel, on bool) error {
	name := m.Name.ValueString()
	if !on {
		var err error
		if name, err = builtInDefaultProfile(ctx, r.c(), m.Language.ValueString()); err != nil {
			return err
		}
	}
	_, err := r.c().Qualityprofiles.SetDefault(ctx, &sonarqube.QualityprofilesSetDefaultOptions{Language: m.Language.ValueString(), QualityProfile: name})
	return err
}

func (r *qualityProfileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan qualityProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.checkParent(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Invalid parent for quality profile "+plan.Name.ValueString(), err)
		return
	}
	id := plan.Language.ValueString() + "/" + plan.Name.ValueString()
	key, err := r.createProfile(ctx, &plan)
	if err != nil {
		createError(&resp.Diagnostics, "quality profile", id, err)
		return
	}
	state := plan
	state.ID, state.Key = types.StringValue(key), types.StringValue(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if !plan.Backup.IsNull() {
		if err := r.restore(ctx, &plan); err != nil {
			apiError(&resp.Diagnostics, "Unable to restore quality profile "+plan.Name.ValueString()+" from its backup", err)
			return
		}
	}
	if !plan.Parent.IsNull() {
		if err := r.setParent(ctx, &plan); err != nil {
			apiError(&resp.Diagnostics, "Unable to set the parent of quality profile "+plan.Name.ValueString(), err)
			return
		}
	}
	if plan.IsDefault.ValueBool() {
		if err := r.setDefault(ctx, &plan, true); err != nil {
			apiError(&resp.Diagnostics, "Unable to make quality profile "+plan.Name.ValueString()+" the default", err)
			return
		}
	}
	if ok, err := r.readInto(ctx, &state); err != nil || !ok {
		resp.Diagnostics.AddError("Unable to read quality profile after creation", fmt.Sprint(err))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// createProfile creates an empty profile, or a copy of copy_from. Copy and
// restore would overwrite a profile that already has the name, so that is
// refused here.
func (r *qualityProfileResource) createProfile(ctx context.Context, m *qualityProfileModel) (string, error) {
	lang, name := m.Language.ValueString(), m.Name.ValueString()
	if !m.CopyFrom.IsNull() || !m.Backup.IsNull() {
		existing, err := findProfile(ctx, r.c(), "", lang, name)
		if err != nil {
			return "", err
		}
		if existing != nil {
			return "", fmt.Errorf("a %s quality profile named %q already exists", lang, name)
		}
	}
	if !m.CopyFrom.IsNull() {
		return r.copyProfile(ctx, m)
	}
	res, _, err := r.c().Qualityprofiles.Create(ctx, &sonarqube.QualityprofilesCreateOptions{Name: name, Language: lang})
	if err != nil {
		return "", err
	}
	return res.Profile.Key, nil
}

func (r *qualityProfileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state qualityProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.readInto(ctx, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality profile "+state.Name.ValueString(), err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	if !state.Backup.IsNull() {
		raw, got, err := r.exportBackup(ctx, &state)
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to back up quality profile "+state.Name.ValueString(), err)
			return
		}
		// Keep the configured form while the rules match it; otherwise
		// record what the server has, so the plan restores the backup.
		if want, err := parseBackup(state.Backup.ValueString()); err != nil || want.Language != got.Language {
			state.Backup = types.StringValue(raw)
		} else if missing, extra, changed := backupDiff(want, got); len(missing)+len(extra)+len(changed) > 0 {
			state.Backup = types.StringValue(raw)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *qualityProfileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state qualityProfileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Name.Equal(state.Name) {
		if _, err := r.c().Qualityprofiles.Rename(ctx, &sonarqube.QualityprofilesRenameOptions{Key: state.ID.ValueString(), Name: plan.Name.ValueString()}); err != nil {
			apiError(&resp.Diagnostics, "Unable to rename quality profile "+state.Name.ValueString(), err)
			return
		}
	}
	if !plan.Parent.Equal(state.Parent) {
		if err := r.setParent(ctx, &plan); err != nil {
			apiError(&resp.Diagnostics, "Unable to change the parent of quality profile "+plan.Name.ValueString(), err)
			return
		}
	}
	if !plan.Backup.IsNull() && !plan.Backup.Equal(state.Backup) {
		if err := r.restore(ctx, &plan); err != nil {
			apiError(&resp.Diagnostics, "Unable to restore quality profile "+plan.Name.ValueString()+" from its backup", err)
			return
		}
	}
	if !plan.IsDefault.Equal(state.IsDefault) {
		if err := r.setDefault(ctx, &plan, plan.IsDefault.ValueBool()); err != nil {
			apiError(&resp.Diagnostics, "Unable to change the default quality profile", err)
			return
		}
	}
	plan.ID = state.ID
	if ok, err := r.readInto(ctx, &plan); err != nil || !ok {
		resp.Diagnostics.AddError("Unable to read quality profile after update", fmt.Sprint(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *qualityProfileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state qualityProfileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name, lang := state.Name.ValueString(), state.Language.ValueString()
	// Held from the child check to the delete, so no child is attached in between.
	unlock := lockProfileTree(r.p)
	defer unlock()
	inh, _, err := r.c().Qualityprofiles.Inheritance(ctx, &sonarqube.QualityprofilesInheritanceOptions{Language: lang, QualityProfile: name})
	if isGone(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to check child profiles of "+name, err)
		return
	}
	if len(inh.Children) > 0 {
		var kids []string
		for _, c := range inh.Children {
			kids = append(kids, c.Name)
		}
		resp.Diagnostics.AddError("Quality profile has child profiles",
			fmt.Sprintf("Deleting %q would also delete its child profiles %s. Re-parent or destroy them first.", name, strings.Join(kids, ", ")))
		return
	}
	if p, err := findProfile(ctx, r.c(), state.ID.ValueString(), lang, ""); err == nil && p != nil && p.IsDefault {
		if err := r.setDefault(ctx, &state, false); err != nil {
			apiError(&resp.Diagnostics, "Unable to move the default away from "+name, err)
			return
		}
	}
	_, err = r.c().Qualityprofiles.Delete(ctx, &sonarqube.QualityprofilesDeleteOptions{Language: lang, QualityProfile: name})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete quality profile "+name, err)
	}
}

// ImportState accepts the profile key, or `language/name`.
func (r *qualityProfileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, lang, name := req.ID, "", ""
	if l, n, ok := strings.Cut(req.ID, "/"); ok {
		key, lang, name = "", l, n
	}
	p, err := findProfile(ctx, r.c(), key, lang, name)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to import quality profile", err)
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("Quality profile not found", fmt.Sprintf("No profile matches %q (use the profile key or language/name).", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), p.Key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("language"), p.Language)...)
}

// ---------------------------------------------------------------------------
// sonarqube_qualityprofile_project_association

type qualityProfileProjectResource struct{ resourceBase }

func newQualityProfileProjectAssociationResource() resource.Resource {
	return &qualityProfileProjectResource{}
}

type qualityProfileProjectModel struct {
	ID             types.String `tfsdk:"id"`
	QualityProfile types.String `tfsdk:"quality_profile"`
	Project        types.String `tfsdk:"project"`
	Language       types.String `tfsdk:"language"`
}

func (r *qualityProfileProjectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofile_project_association"
}

func (r *qualityProfileProjectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Assigns a quality profile to a project for one language. Import ID: `language/profile/project`.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state."},
			"quality_profile": schema.StringAttribute{Required: true, Description: "Profile name. Changing it reassigns in place."},
			"project":         schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project key."},
			"language":        schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Language key."},
		},
	}
}

func (m *qualityProfileProjectModel) id() types.String {
	return types.StringValue(m.QualityProfile.ValueString() + "/" + m.Project.ValueString())
}

func (r *qualityProfileProjectResource) add(ctx context.Context, m *qualityProfileProjectModel) error {
	_, err := r.c().Qualityprofiles.AddProject(ctx, &sonarqube.QualityprofilesAddProjectOptions{
		Language: m.Language.ValueString(), QualityProfile: m.QualityProfile.ValueString(), Project: m.Project.ValueString()})
	return err
}

func (r *qualityProfileProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan qualityProfileProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.add(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to assign quality profile", err)
		return
	}
	plan.ID = plan.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read checks for an explicit assignment: a project merely falling back to
// the default profile is not associated.
func (r *qualityProfileProjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state qualityProfileProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: state.Language.ValueString(), Project: state.Project.ValueString()})
	if isGone(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality profile of "+state.Project.ValueString(), err)
		return
	}
	var key, name string
	for _, p := range res.Profiles {
		if p.Language == state.Language.ValueString() {
			key, name = p.Key, p.Name
		}
	}
	explicit := false
	if key != "" {
		explicit, err = selectedContains(ctx, state.Project.ValueString(), func(ctx context.Context, pg, ps int) ([]*sonarqube.QualityprofilesProjectsResponseResult, int64, error) {
			res, _, err := r.c().Qualityprofiles.Projects(ctx, &sonarqube.QualityprofilesProjectsOptions{Key: key, Q: state.Project.ValueString(), Selected: "selected", P: &pg, Ps: &ps})
			if err != nil {
				return nil, 0, err
			}
			return res.Results, total(res.Paging), nil
		}, func(p *sonarqube.QualityprofilesProjectsResponseResult) string { return p.Key })
		if err != nil {
			apiError(&resp.Diagnostics, "Unable to read projects of quality profile "+name, err)
			return
		}
	}
	if !explicit {
		resp.State.RemoveResource(ctx)
		return
	}
	state.QualityProfile = types.StringValue(name)
	state.ID = state.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *qualityProfileProjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan qualityProfileProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.add(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to assign quality profile", err)
		return
	}
	plan.ID = plan.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *qualityProfileProjectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state qualityProfileProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().Qualityprofiles.RemoveProject(ctx, &sonarqube.QualityprofilesRemoveProjectOptions{
		Language: state.Language.ValueString(), QualityProfile: state.QualityProfile.ValueString(), Project: state.Project.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to unassign quality profile", err)
	}
}

func (r *qualityProfileProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	first, last := strings.Index(req.ID, "/"), strings.LastIndex(req.ID, "/")
	if first <= 0 || last <= first+1 || last == len(req.ID)-1 {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected `language/profile/project`, got %q.", req.ID))
		return
	}
	m := qualityProfileProjectModel{Language: types.StringValue(req.ID[:first]),
		QualityProfile: types.StringValue(req.ID[first+1 : last]), Project: types.StringValue(req.ID[last+1:])}
	m.ID = m.id()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_qualityprofile_activate_rule

type activateRuleResource struct{ resourceBase }

func newQualityProfileActivateRuleResource() resource.Resource { return &activateRuleResource{} }

type activateRuleModel struct {
	ID       types.String `tfsdk:"id"`
	Key      types.String `tfsdk:"key"`
	Rule     types.String `tfsdk:"rule"`
	Severity types.String `tfsdk:"severity"`
	Params   types.String `tfsdk:"params"`
	Reset    types.String `tfsdk:"reset"`
}

var severities = []string{"INFO", "MINOR", "MAJOR", "CRITICAL", "BLOCKER"}

func (r *activateRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofile_activate_rule"
}

func (r *activateRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Activates a rule in a quality profile. Severity and parameters change in place. Import ID: `profile_key/rule_key`.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key":  schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Quality profile key."},
			"rule": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Rule key, e.g. `java:S1144`."},
			"severity": schema.StringAttribute{Optional: true, Description: "Severity override. When omitted, the rule's default applies and drift is not tracked.",
				Validators: []validator.String{stringvalidator.OneOf(severities...)}},
			"params": schema.StringAttribute{Optional: true,
				Description: "Parameters as `key1=value1;key2=value2`. When omitted, parameters are not tracked."},
			"reset": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("false"),
				Description: "Reset severity and parameters of an inherited rule to the parent's values (`true`/`false`).",
				Validators:  []validator.String{stringvalidator.OneOf("true", "false")}},
		},
	}
}

func (r *activateRuleResource) activate(ctx context.Context, m *activateRuleModel) error {
	opts := &sonarqube.QualityprofilesActivateRuleOptions{Key: m.Key.ValueString(), Rule: m.Rule.ValueString(),
		Severity: m.Severity.ValueString(), Params: m.Params.ValueString()}
	if m.Reset.ValueString() == "true" {
		opts.Reset = new(true)
	}
	_, err := r.c().Qualityprofiles.ActivateRule(ctx, opts)
	return err
}

// activeIn returns the activation of rule in profile, or nil.
func activeIn(ctx context.Context, c *sonarqube.Client, rule, profile string) (*sonarqube.RulesShowResponseActive, error) {
	res, _, err := c.Rules.Show(ctx, &sonarqube.RulesShowOptions{Key: rule, Actives: new(true)})
	if err != nil {
		return nil, err
	}
	for _, a := range res.Actives {
		if a.QProfile == profile {
			return a, nil
		}
	}
	return nil, nil
}

func parseParams(s string) map[string]string {
	out := map[string]string{}
	for kv := range strings.SplitSeq(s, ";") {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}

func (r *activateRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan activateRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	if err := r.activate(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to activate rule "+plan.Rule.ValueString(), err)
		return
	}
	plan.ID = types.StringValue(plan.Key.ValueString() + "/" + plan.Rule.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *activateRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state activateRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := activeIn(ctx, r.c(), state.Rule.ValueString(), state.Key.ValueString())
	if isGone(err) || (err == nil && a == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+state.Rule.ValueString(), err)
		return
	}
	if !state.Severity.IsNull() {
		state.Severity = types.StringValue(a.Severity)
	}
	if !state.Params.IsNull() {
		have := map[string]string{}
		for _, p := range a.Params {
			have[p.Key] = p.Value
		}
		want := parseParams(state.Params.ValueString())
		// Compare only configured keys: the server also reports defaults.
		same := true
		for k, v := range want {
			if have[k] != v {
				same = false
			}
		}
		if !same {
			keys := slices.Sorted(maps.Keys(want))
			var parts []string
			for _, k := range keys {
				parts = append(parts, k+"="+have[k])
			}
			state.Params = types.StringValue(strings.Join(parts, ";"))
		}
	}
	state.ID = types.StringValue(state.Key.ValueString() + "/" + state.Rule.ValueString())
	if state.Reset.IsNull() {
		state.Reset = types.StringValue("false")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *activateRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan activateRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	// Re-activating a rule cascades to descendant profiles and would undo
	// their explicit deactivation of it. Remember which descendants have it
	// inactive, and restore that afterwards.
	keep, err := descendantsWithoutRule(ctx, r.c(), plan.Key.ValueString(), plan.Rule.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to inspect child profiles of "+plan.Key.ValueString(), err)
		return
	}
	if err := r.activate(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to update rule "+plan.Rule.ValueString(), err)
		return
	}
	for _, child := range keep {
		_, err := r.c().Qualityprofiles.DeactivateRule(ctx, &sonarqube.QualityprofilesDeactivateRuleOptions{Key: child, Rule: plan.Rule.ValueString()})
		if err != nil && !isGone(err) {
			apiError(&resp.Diagnostics, fmt.Sprintf("Unable to keep rule %s deactivated in child profile %s", plan.Rule.ValueString(), child), err)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// descendantsWithoutRule returns the keys of profiles descending from
// profile in which rule is not active.
func descendantsWithoutRule(ctx context.Context, c *sonarqube.Client, profile, rule string) ([]string, error) {
	all, _, err := c.Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{})
	if err != nil {
		return nil, err
	}
	children := map[string][]string{}
	for _, p := range all.Profiles {
		if p.ParentKey != "" {
			children[p.ParentKey] = append(children[p.ParentKey], p.Key)
		}
	}
	if len(children[profile]) == 0 {
		return nil, nil
	}
	res, _, err := c.Rules.Show(ctx, &sonarqube.RulesShowOptions{Key: rule, Actives: new(true)})
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, a := range res.Actives {
		active[a.QProfile] = true
	}
	var out []string
	queue := slices.Clone(children[profile])
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if !active[k] {
			out = append(out, k)
		}
		queue = append(queue, children[k]...)
	}
	return out, nil
}

func (r *activateRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state activateRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	_, err := r.c().Qualityprofiles.DeactivateRule(ctx, &sonarqube.QualityprofilesDeactivateRuleOptions{Key: state.Key.ValueString(), Rule: state.Rule.ValueString()})
	if err != nil && hasMessage(err, "inherited") {
		// An inherited rule cannot be deactivated; revert our override instead.
		_, err = r.c().Qualityprofiles.ActivateRule(ctx, &sonarqube.QualityprofilesActivateRuleOptions{Key: state.Key.ValueString(), Rule: state.Rule.ValueString(), Reset: new(true)})
	}
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to deactivate rule "+state.Rule.ValueString(), err)
	}
}

func (r *activateRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, "/", 2, "profile_key/rule_key")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	a, err := activeIn(ctx, r.c(), parts[1], parts[0])
	if err != nil || a == nil {
		resp.Diagnostics.AddError("Rule not active", fmt.Sprintf("Rule %s is not active in profile %s (%v).", parts[1], parts[0], err))
		return
	}
	m := activateRuleModel{ID: types.StringValue(req.ID), Key: types.StringValue(parts[0]), Rule: types.StringValue(parts[1]),
		Severity: types.StringValue(a.Severity), Reset: types.StringValue("false"), Params: types.StringNull()}
	if len(a.Params) > 0 {
		var ps []string
		for _, p := range a.Params {
			ps = append(ps, p.Key+"="+p.Value)
		}
		m.Params = types.StringValue(strings.Join(ps, ";"))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_qualityprofile_deactivate_rule

type deactivateRuleResource struct{ resourceBase }

func newQualityProfileDeactivateRuleResource() resource.Resource { return &deactivateRuleResource{} }

type deactivateRuleModel struct {
	ID   types.String `tfsdk:"id"`
	Key  types.String `tfsdk:"key"`
	Rule types.String `tfsdk:"rule"`
}

func (r *deactivateRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualityprofile_deactivate_rule"
}

func (r *deactivateRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Ensures a rule is not active in a quality profile, e.g. one a parent profile would activate. Destroying it activates the rule again.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key":  schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Quality profile key."},
			"rule": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Rule key."},
		},
	}
}

func (r *deactivateRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deactivateRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	_, err := r.c().Qualityprofiles.DeactivateRule(ctx, &sonarqube.QualityprofilesDeactivateRuleOptions{Key: plan.Key.ValueString(), Rule: plan.Rule.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to deactivate rule "+plan.Rule.ValueString(), err)
		return
	}
	plan.ID = types.StringValue(plan.Key.ValueString() + "/" + plan.Rule.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *deactivateRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deactivateRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := activeIn(ctx, r.c(), state.Rule.ValueString(), state.Key.ValueString())
	// Active again (or the rule is gone): the deactivation no longer holds.
	if isGone(err) || (err == nil && a != nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+state.Rule.ValueString(), err)
		return
	}
	state.ID = types.StringValue(state.Key.ValueString() + "/" + state.Rule.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *deactivateRuleResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *deactivateRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state deactivateRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := lockProfileTree(r.p)
	defer unlock()
	_, err := r.c().Qualityprofiles.ActivateRule(ctx, &sonarqube.QualityprofilesActivateRuleOptions{Key: state.Key.ValueString(), Rule: state.Rule.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to reactivate rule "+state.Rule.ValueString(), err)
	}
}

func (r *deactivateRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, "/", 2, "profile_key/rule_key")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &deactivateRuleModel{ID: types.StringValue(req.ID), Key: types.StringValue(parts[0]), Rule: types.StringValue(parts[1])})...)
}

func (r *qualityProfileResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *qualityProfileProjectResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *activateRuleResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *deactivateRuleResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
