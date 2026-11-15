package provider

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// data sonarqube_project_branches

type projectBranchesDataSource struct{ dataSourceBase }

func newProjectBranchesDataSource() datasource.DataSource { return &projectBranchesDataSource{} }

type projectBranchModel struct {
	Name              types.String `tfsdk:"name"`
	IsMain            types.Bool   `tfsdk:"is_main"`
	Type              types.String `tfsdk:"type"`
	AnalysisDate      types.String `tfsdk:"analysis_date"`
	QualityGateStatus types.String `tfsdk:"quality_gate_status"`
	ExcludedFromPurge types.Bool   `tfsdk:"excluded_from_purge"`
}

type projectBranchesModel struct {
	ID         types.String         `tfsdk:"id"`
	Project    types.String         `tfsdk:"project"`
	MainBranch types.String         `tfsdk:"main_branch"`
	Branches   []projectBranchModel `tfsdk:"branches"`
}

func (d *projectBranchesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_branches"
}

func (d *projectBranchesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the branches of a project, main branch first, then by name. Branches other than the main " +
			"branch exist once analyzed, and need the Developer edition or above.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "The project key."},
			"project":     schema.StringAttribute{Required: true, Description: "Project key."},
			"main_branch": schema.StringAttribute{Computed: true, Description: "Name of the main branch."},
			"branches": schema.ListNestedAttribute{Computed: true, Description: "Branches of the project.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":                schema.StringAttribute{Computed: true, Description: "Branch name."},
					"is_main":             schema.BoolAttribute{Computed: true, Description: "Whether this is the main branch."},
					"type":                schema.StringAttribute{Computed: true, Description: "`BRANCH`."},
					"analysis_date":       schema.StringAttribute{Computed: true, Description: "Date of the last analysis; null if never analyzed."},
					"quality_gate_status": schema.StringAttribute{Computed: true, Description: "`OK` or `ERROR`; null if never analyzed."},
					"excluded_from_purge": schema.BoolAttribute{Computed: true, Description: "Whether the branch is kept when inactive."},
				}}},
		},
	}
}

func (d *projectBranchesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m projectBranchesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.Project.ValueString()
	res, _, err := d.c().ProjectBranches.List(ctx, &sonarqube.ProjectBranchesListOptions{Project: key})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to list branches of project "+key, err)
		return
	}
	branches := slices.Clone(res.Branches)
	slices.SortFunc(branches, func(a, b *sonarqube.ProjectBranchesListResponseBranch) int {
		if a.IsMain != b.IsMain {
			if a.IsMain {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	m.Branches = make([]projectBranchModel, 0, len(branches))
	m.MainBranch = types.StringNull()
	for _, b := range branches {
		status := ""
		if b.Status != nil {
			status = b.Status.QualityGateStatus
		}
		if b.IsMain {
			m.MainBranch = types.StringValue(b.Name)
		}
		m.Branches = append(m.Branches, projectBranchModel{
			Name: types.StringValue(b.Name), IsMain: types.BoolValue(b.IsMain), Type: types.StringValue(b.Type),
			AnalysisDate: strOrNull(b.AnalysisDate), QualityGateStatus: strOrNull(status),
			ExcludedFromPurge: types.BoolValue(b.ExcludedFromPurge),
		})
	}
	m.ID = types.StringValue(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// data sonarqube_quality_gate_status

type qualityGateStatusDataSource struct{ dataSourceBase }

func newQualityGateStatusDataSource() datasource.DataSource { return &qualityGateStatusDataSource{} }

type gateConditionStatusModel struct {
	Metric         types.String `tfsdk:"metric"`
	Comparator     types.String `tfsdk:"comparator"`
	ErrorThreshold types.String `tfsdk:"error_threshold"`
	ActualValue    types.String `tfsdk:"actual_value"`
	Status         types.String `tfsdk:"status"`
}

type qualityGateStatusModel struct {
	ID                types.String               `tfsdk:"id"`
	Project           types.String               `tfsdk:"project"`
	Branch            types.String               `tfsdk:"branch"`
	PullRequest       types.String               `tfsdk:"pull_request"`
	Status            types.String               `tfsdk:"status"`
	IgnoredConditions types.Bool                 `tfsdk:"ignored_conditions"`
	Conditions        []gateConditionStatusModel `tfsdk:"conditions"`
}

func (d *qualityGateStatusDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_quality_gate_status"
}

func (d *qualityGateStatusDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The quality gate status of a project's last analysis, for its main branch or a given branch or pull " +
			"request. Use it in preconditions and checks, e.g. to hold a deployment while the gate fails.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "`project`, `project:branch` or `project#pull_request`."},
			"project": schema.StringAttribute{Required: true, Description: "Project key."},
			"branch": schema.StringAttribute{Optional: true, Description: "Branch name. Defaults to the main branch.",
				Validators: []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("pull_request"))}},
			"pull_request": schema.StringAttribute{Optional: true, Description: "Pull request ID."},
			"status":       schema.StringAttribute{Computed: true, Description: "`OK`, `ERROR`, or `NONE` when not analyzed yet."},
			"ignored_conditions": schema.BoolAttribute{Computed: true,
				Description: "Whether conditions on coverage and duplication were ignored because little code changed."},
			"conditions": schema.ListNestedAttribute{Computed: true, Description: "Evaluated conditions, sorted by metric.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"metric":          schema.StringAttribute{Computed: true, Description: "Metric key."},
					"comparator":      schema.StringAttribute{Computed: true, Description: "`LT` or `GT`."},
					"error_threshold": schema.StringAttribute{Computed: true, Description: "Threshold of the condition."},
					"actual_value":    schema.StringAttribute{Computed: true, Description: "Measured value."},
					"status":          schema.StringAttribute{Computed: true, Description: "`OK` or `ERROR`."},
				}}},
		},
	}
}

func (d *qualityGateStatusDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m qualityGateStatusModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.Project.ValueString()
	res, _, err := d.c().Qualitygates.ProjectStatus(ctx, &sonarqube.QualitygatesProjectStatusOptions{
		ProjectKey: key, Branch: m.Branch.ValueString(), PullRequest: m.PullRequest.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read the quality gate status of project "+key, err)
		return
	}
	ps := res.ProjectStatus
	if ps == nil {
		ps = &sonarqube.QualitygatesProjectStatusResponseProjectStatus{Status: "NONE"}
	}
	conds := slices.Clone(ps.Conditions)
	slices.SortFunc(conds, func(a, b *sonarqube.QualitygatesProjectStatusResponseProjectStatusCondition) int {
		return cmp.Compare(a.MetricKey, b.MetricKey)
	})
	m.Conditions = make([]gateConditionStatusModel, 0, len(conds))
	for _, c := range conds {
		m.Conditions = append(m.Conditions, gateConditionStatusModel{
			Metric: types.StringValue(c.MetricKey), Comparator: types.StringValue(c.Comparator),
			ErrorThreshold: types.StringValue(c.ErrorThreshold), ActualValue: types.StringValue(c.ActualValue),
			Status: types.StringValue(c.Status),
		})
	}
	m.Status = types.StringValue(ps.Status)
	m.IgnoredConditions = types.BoolValue(ps.IgnoredConditions)
	id := key
	switch {
	case !m.Branch.IsNull():
		id += ":" + m.Branch.ValueString()
	case !m.PullRequest.IsNull():
		id += "#" + m.PullRequest.ValueString()
	}
	m.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
