package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// sonarqube_qualitygate

var (
	_ resource.ResourceWithModifyPlan     = (*qualityGateResource)(nil)
	_ resource.ResourceWithValidateConfig = (*qualityGateResource)(nil)
)

type qualityGateResource struct{ resourceBase }

func newQualityGateResource() resource.Resource { return &qualityGateResource{} }

type qualityGateModel struct {
	ID        types.String     `tfsdk:"id"`
	Name      types.String     `tfsdk:"name"`
	CopyFrom  types.String     `tfsdk:"copy_from"`
	IsDefault types.Bool       `tfsdk:"is_default"`
	Condition []conditionModel `tfsdk:"condition"`
}

type conditionModel struct {
	ID        types.String `tfsdk:"id"`
	Metric    types.String `tfsdk:"metric"`
	Op        types.String `tfsdk:"op"`
	Threshold types.String `tfsdk:"threshold"`
}

func (r *qualityGateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualitygate"
}

func (r *qualityGateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: schemaVersion,
		Description: "A quality gate. Conditions are authoritative when at least one `condition` block is present; " +
			"a gate created with `copy_from` and no blocks keeps the copied conditions unmanaged. " +
			"Import ID: the gate name. An imported gate manages all its conditions, in metric order; list the " +
			"`condition` blocks in that order to avoid a reordering diff after import.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "The gate name."},
			"name": schema.StringAttribute{Required: true, Description: "Gate name. Changing it renames the gate in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100), noSurroundingSpace{}}},
			"copy_from": schema.StringAttribute{Optional: true, Description: "Name of a gate to copy on creation.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"is_default": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Make this the default gate. Unsetting it, or destroying the gate while default, makes the built-in gate the default again."},
		},
		Blocks: map[string]schema.Block{
			"condition": schema.ListNestedBlock{
				Description: "A gate condition. Metrics must be unique within a gate.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"id":     schema.StringAttribute{Computed: true, Description: "Condition ID."},
					"metric": schema.StringAttribute{Required: true, Description: "Metric key, e.g. `new_coverage`."},
					"op": schema.StringAttribute{Required: true, Description: "`GT` (fails above threshold) or `LT` (fails below).",
						Validators: []validator.String{stringvalidator.OneOf("GT", "LT")}},
					"threshold": schema.StringAttribute{Required: true, Description: "Error threshold."},
				}},
			},
		},
	}
}

func (r *qualityGateResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg qualityGateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.CopyFrom.IsNull() && len(cfg.Condition) == 0 {
		resp.Diagnostics.AddError("Missing conditions", "Set copy_from or add at least one condition block.")
	}
	seen := map[string]bool{}
	for _, c := range cfg.Condition {
		if c.Metric.IsUnknown() {
			continue
		}
		if seen[c.Metric.ValueString()] {
			resp.Diagnostics.AddError("Duplicate condition", fmt.Sprintf("Metric %q has more than one condition; SonarQube allows one per metric.", c.Metric.ValueString()))
		}
		seen[c.Metric.ValueString()] = true
	}
}

func (r *qualityGateResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan qualityGateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if plan.Name.Equal(state.Name) {
		resp.Plan.SetAttribute(ctx, path.Root("id"), state.ID)
	}
	// Keep known condition IDs for unchanged conditions to avoid noise.
	byMetric := map[string]conditionModel{}
	for _, c := range state.Condition {
		byMetric[c.Metric.ValueString()] = c
	}
	for i, c := range plan.Condition {
		if s, ok := byMetric[c.Metric.ValueString()]; ok && s.Op.Equal(c.Op) && s.Threshold.Equal(c.Threshold) {
			resp.Plan.SetAttribute(ctx, path.Root("condition").AtListIndex(i).AtName("id"), s.ID)
		}
	}
}

func (r *qualityGateResource) show(ctx context.Context, name string) (*sonarqube.QualitygatesShowResponse, error) {
	g, _, err := r.c().Qualitygates.Show(ctx, &sonarqube.QualitygatesShowOptions{Name: name})
	return g, err
}

// syncConditions makes the gate's conditions equal to want, by metric.
func (r *qualityGateResource) syncConditions(ctx context.Context, gate string, want []conditionModel, diags *diag.Diagnostics) {
	unlock := r.p.locks.lock("gate:" + gate)
	defer unlock()
	g, err := r.show(ctx, gate)
	if err != nil {
		apiError(diags, "Unable to read quality gate "+gate, err)
		return
	}
	have := map[string]*sonarqube.QualitygatesShowResponseCondition{}
	for _, c := range g.Conditions {
		have[c.Metric] = c
	}
	wanted := map[string]bool{}
	for _, c := range want {
		m, op, th := c.Metric.ValueString(), c.Op.ValueString(), c.Threshold.ValueString()
		wanted[m] = true
		cur, ok := have[m]
		switch {
		case !ok:
			_, _, err = r.c().Qualitygates.CreateCondition(ctx, &sonarqube.QualitygatesCreateConditionOptions{GateName: gate, Metric: m, Op: op, Error: th})
		case cur.Op != op || cur.Error != th:
			_, err = r.c().Qualitygates.UpdateCondition(ctx, &sonarqube.QualitygatesUpdateConditionOptions{ID: string(cur.ID), Metric: m, Op: op, Error: th})
		}
		if err != nil {
			apiError(diags, fmt.Sprintf("Unable to set condition %s on quality gate %s", m, gate), err)
			return
		}
	}
	for m, c := range have {
		if wanted[m] {
			continue
		}
		if _, err := r.c().Qualitygates.DeleteCondition(ctx, &sonarqube.QualitygatesDeleteConditionOptions{ID: string(c.ID)}); err != nil && !isGone(err) {
			apiError(diags, fmt.Sprintf("Unable to delete condition %s from quality gate %s", m, gate), err)
			return
		}
	}
}

// builtInGate returns the name of the built-in gate, which is made default
// when this gate stops being the default.
func builtInGate(ctx context.Context, c *sonarqube.Client) (string, error) {
	l, _, err := c.Qualitygates.List(ctx)
	if err != nil {
		return "", err
	}
	for _, g := range l.Qualitygates {
		if g.IsBuiltIn {
			return g.Name, nil
		}
	}
	return "", fmt.Errorf("no built-in quality gate found")
}

func (r *qualityGateResource) setDefault(ctx context.Context, name string, on bool) error {
	if !on {
		var err error
		if name, err = builtInGate(ctx, r.c()); err != nil {
			return err
		}
	}
	_, err := r.c().Qualitygates.SetDefault(ctx, &sonarqube.QualitygatesSetDefaultOptions{Name: name})
	return err
}

// readInto refreshes m. Conditions are reported in configuration order; when
// conditions are unmanaged, they stay empty.
func (r *qualityGateResource) readInto(ctx context.Context, m *qualityGateModel, diags *diag.Diagnostics) bool {
	g, err := r.show(ctx, m.ID.ValueString())
	if isGone(err) {
		return false
	}
	if err != nil {
		apiError(diags, "Unable to read quality gate "+m.ID.ValueString(), err)
		return true
	}
	m.ID = types.StringValue(g.Name)
	m.Name = types.StringValue(g.Name)
	m.IsDefault = types.BoolValue(g.IsDefault)
	if len(m.Condition) > 0 {
		byMetric := map[string]*sonarqube.QualitygatesShowResponseCondition{}
		for _, c := range g.Conditions {
			byMetric[c.Metric] = c
		}
		var out []conditionModel
		used := map[string]bool{}
		for _, c := range m.Condition {
			if s, ok := byMetric[c.Metric.ValueString()]; ok {
				out = append(out, conditionFrom(s))
				used[s.Metric] = true
			}
		}
		for _, s := range g.Conditions {
			if !used[s.Metric] {
				out = append(out, conditionFrom(s))
			}
		}
		m.Condition = out
	}
	return true
}

func conditionFrom(c *sonarqube.QualitygatesShowResponseCondition) conditionModel {
	return conditionModel{ID: types.StringValue(string(c.ID)), Metric: types.StringValue(c.Metric),
		Op: types.StringValue(c.Op), Threshold: types.StringValue(c.Error)}
}

func (r *qualityGateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan qualityGateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	var err error
	if !plan.CopyFrom.IsNull() {
		_, err = r.c().Qualitygates.Copy(ctx, &sonarqube.QualitygatesCopyOptions{Name: name, SourceName: plan.CopyFrom.ValueString()})
	} else {
		_, _, err = r.c().Qualitygates.Create(ctx, &sonarqube.QualitygatesCreateOptions{Name: name})
	}
	if err != nil {
		createError(&resp.Diagnostics, "quality gate", name, err)
		return
	}
	state := plan
	state.ID = types.StringValue(name)
	for i := range state.Condition {
		state.Condition[i].ID = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	if len(plan.Condition) > 0 {
		r.syncConditions(ctx, name, plan.Condition, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if plan.IsDefault.ValueBool() {
		if err := r.setDefault(ctx, name, true); err != nil {
			apiError(&resp.Diagnostics, "Unable to make "+name+" the default quality gate", err)
			return
		}
	}
	if !r.readInto(ctx, &state, &resp.Diagnostics) {
		resp.Diagnostics.AddError("Quality gate disappeared", name+" was not found after creation.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *qualityGateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state qualityGateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.readInto(ctx, &state, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *qualityGateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state qualityGateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	if newName := plan.Name.ValueString(); newName != name {
		if _, err := r.c().Qualitygates.Rename(ctx, &sonarqube.QualitygatesRenameOptions{CurrentName: name, Name: newName}); err != nil {
			apiError(&resp.Diagnostics, "Unable to rename quality gate "+name, err)
			return
		}
		name = newName
		state.ID, state.Name = types.StringValue(name), types.StringValue(name)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
	if len(plan.Condition) > 0 {
		r.syncConditions(ctx, name, plan.Condition, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !plan.IsDefault.Equal(state.IsDefault) {
		if err := r.setDefault(ctx, name, plan.IsDefault.ValueBool()); err != nil {
			apiError(&resp.Diagnostics, "Unable to change the default quality gate", err)
			return
		}
	}
	plan.ID = types.StringValue(name)
	if !r.readInto(ctx, &plan, &resp.Diagnostics) {
		resp.Diagnostics.AddError("Quality gate disappeared", name+" was not found after update.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *qualityGateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state qualityGateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.ID.ValueString()
	// The default gate cannot be deleted; check the live value, not state.
	g, err := r.show(ctx, name)
	if isGone(err) {
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality gate "+name, err)
		return
	}
	if g.IsDefault {
		if err := r.setDefault(ctx, name, false); err != nil {
			apiError(&resp.Diagnostics, "Unable to move the default away from quality gate "+name, err)
			return
		}
	}
	if _, err := r.c().Qualitygates.Delete(ctx, &sonarqube.QualitygatesDeleteOptions{Name: name}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete quality gate "+name, err)
	}
}

func (r *qualityGateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Imported gates manage their conditions: read them all into state.
	g, err := r.show(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to import quality gate "+req.ID, err)
		return
	}
	m := qualityGateModel{ID: types.StringValue(g.Name), Name: types.StringValue(g.Name), CopyFrom: types.StringNull(), IsDefault: types.BoolValue(g.IsDefault)}
	for _, c := range g.Conditions {
		m.Condition = append(m.Condition, conditionFrom(c))
	}
	// The server's order varies by version (creation order on recent
	// releases, unordered on older ones); sort so an import is reproducible.
	slices.SortFunc(m.Condition, func(a, b conditionModel) int {
		return strings.Compare(a.Metric.ValueString(), b.Metric.ValueString())
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// sonarqube_qualitygate_project_association

type qualityGateProjectAssociationResource struct{ resourceBase }

func newQualityGateProjectAssociationResource() resource.Resource {
	return &qualityGateProjectAssociationResource{}
}

type qualityGateProjectModel struct {
	ID         types.String `tfsdk:"id"`
	GateID     types.String `tfsdk:"gateid"`
	GateName   types.String `tfsdk:"gatename"`
	ProjectKey types.String `tfsdk:"projectkey"`
}

func (r *qualityGateProjectAssociationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_qualitygate_project_association"
}

func (r *qualityGateProjectAssociationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "Assigns a quality gate to a project. Changing the gate reassigns it in place. Import ID: `gatename/projectkey`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Identifier of this resource in Terraform state."},
			"gateid": schema.StringAttribute{Optional: true, DeprecationMessage: "Gate IDs no longer exist in SonarQube; use gatename.",
				Description: "Ignored."},
			"gatename":   schema.StringAttribute{Required: true, Description: "Gate name."},
			"projectkey": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Project key."},
		},
	}
}

func (r *qualityGateProjectAssociationResource) assign(ctx context.Context, m *qualityGateProjectModel) error {
	_, err := r.c().Qualitygates.Assign(ctx, &sonarqube.QualitygatesAssignOptions{GateName: m.GateName.ValueString(), ProjectKey: m.ProjectKey.ValueString()})
	return err
}

func (r *qualityGateProjectAssociationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan qualityGateProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.assign(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to assign quality gate", err)
		return
	}
	plan.ID = types.StringValue(plan.GateName.ValueString() + "/" + plan.ProjectKey.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *qualityGateProjectAssociationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state qualityGateProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, _, err := r.c().Qualitygates.GetByProject(ctx, &sonarqube.QualitygatesGetByProjectOptions{Project: state.ProjectKey.ValueString()})
	// "default" means the project only falls back to the default gate: no
	// explicit association exists any more.
	if isGone(err) || (err == nil && (res.QualityGate == nil || res.QualityGate.Default)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read quality gate of project "+state.ProjectKey.ValueString(), err)
		return
	}
	state.GateName = types.StringValue(res.QualityGate.Name)
	state.ID = types.StringValue(res.QualityGate.Name + "/" + state.ProjectKey.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *qualityGateProjectAssociationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan qualityGateProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.assign(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to assign quality gate", err)
		return
	}
	plan.ID = types.StringValue(plan.GateName.ValueString() + "/" + plan.ProjectKey.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *qualityGateProjectAssociationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state qualityGateProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c().Qualitygates.Unassign(ctx, &sonarqube.QualitygatesUnassignOptions{ProjectKey: state.ProjectKey.ValueString()})
	if err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to unassign quality gate", err)
	}
}

func (r *qualityGateProjectAssociationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	i := strings.LastIndex(req.ID, "/")
	if i <= 0 || i == len(req.ID)-1 {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected `gatename/projectkey`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gatename"), req.ID[:i])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("projectkey"), req.ID[i+1:])...)
}

func (r *qualityGateResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}

func (r *qualityGateProjectAssociationResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
