package provider

import (
	"context"
	"strings"

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

type ruleResource struct{ resourceBase }

func newRuleResource() resource.Resource { return &ruleResource{} }

type ruleModel struct {
	ID                  types.String `tfsdk:"id"`
	CustomKey           types.String `tfsdk:"custom_key"`
	TemplateKey         types.String `tfsdk:"template_key"`
	Name                types.String `tfsdk:"name"`
	MarkdownDescription types.String `tfsdk:"markdown_description"`
	Params              types.String `tfsdk:"params"`
	PreventReactivation types.String `tfsdk:"prevent_reactivation"`
	Severity            types.String `tfsdk:"severity"`
	Status              types.String `tfsdk:"status"`
	Type                types.String `tfsdk:"type"`
}

func (r *ruleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rule"
}

func (r *ruleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Version:     schemaVersion,
		Description: "A custom rule created from a rule template. Name, description, parameters, severity and status change in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Rule key, `<repository>:<custom_key>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"custom_key":   schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Key of the custom rule within its repository."},
			"template_key": schema.StringAttribute{Required: true, PlanModifiers: replace, Description: "Key of the template rule, e.g. `java:S124`."},
			"name": schema.StringAttribute{Required: true, Description: "Rule name.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
			"markdown_description": schema.StringAttribute{Required: true, Description: "Rule description in Markdown."},
			"params":               schema.StringAttribute{Optional: true, Description: "Parameters as `key1=value1;key2=value2`."},
			"prevent_reactivation": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("false"),
				Description: "Fail rather than reactivate a removed rule with the same key (`true`/`false`).",
				Validators:  []validator.String{stringvalidator.OneOf("true", "false")}},
			"severity": schema.StringAttribute{Optional: true, Computed: true, Description: "Severity.",
				Validators: []validator.String{stringvalidator.OneOf(severities...)}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"status": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("READY"),
				Description: "`READY` (default), `BETA` or `DEPRECATED`.",
				Validators:  []validator.String{stringvalidator.OneOf("READY", "BETA", "DEPRECATED")}},
			"type": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
				Description: "`CODE_SMELL`, `BUG`, `VULNERABILITY` or `SECURITY_HOTSPOT`.",
				Validators:  []validator.String{stringvalidator.OneOf("CODE_SMELL", "BUG", "VULNERABILITY", "SECURITY_HOTSPOT")}},
		},
	}
}

func (r *ruleResource) readInto(ctx context.Context, m *ruleModel) (bool, error) {
	res, _, err := r.c().Rules.Show(ctx, &sonarqube.RulesShowOptions{Key: m.ID.ValueString()})
	if isGone(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ru := res.Rule
	if ru.Status == "REMOVED" {
		return false, nil
	}
	m.Name = types.StringValue(ru.Name)
	m.Severity = strOrNull(ru.Severity)
	m.Status = types.StringValue(ru.Status)
	m.Type = strOrNull(ru.Type)
	if _, ck, ok := strings.Cut(ru.Key, ":"); ok && m.CustomKey.IsNull() {
		m.CustomKey = types.StringValue(ck) // import
	}
	return true, nil
}

func (r *ruleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := &sonarqube.RulesCreateOptions{
		CustomKey: plan.CustomKey.ValueString(), TemplateKey: plan.TemplateKey.ValueString(), Name: plan.Name.ValueString(),
		MarkdownDescription: plan.MarkdownDescription.ValueString(), Params: plan.Params.ValueString(),
		Severity: plan.Severity.ValueString(), Status: plan.Status.ValueString(), Type: plan.Type.ValueString(),
	}
	if plan.PreventReactivation.ValueString() == "true" {
		opts.PreventReactivation = ptr(true)
	}
	res, _, err := r.c().Rules.Create(ctx, opts)
	if err != nil {
		createError(&resp.Diagnostics, "rule", "", err)
		return
	}
	plan.ID = types.StringValue(res.Rule.Key)
	if plan.Severity.IsUnknown() {
		plan.Severity = types.StringNull()
	}
	if plan.Type.IsUnknown() {
		plan.Type = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if _, err := r.readInto(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+res.Rule.Key, err)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ok, err := r.readInto(ctx, &state)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+state.ID.ValueString(), err)
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ruleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, _, err := r.c().Rules.Update(ctx, &sonarqube.RulesUpdateOptions{
		Key: state.ID.ValueString(), Name: plan.Name.ValueString(), MarkdownDescription: plan.MarkdownDescription.ValueString(),
		Params: plan.Params.ValueString(), Severity: plan.Severity.ValueString(), Status: plan.Status.ValueString(),
	})
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to update rule "+state.ID.ValueString(), err)
		return
	}
	plan.ID = state.ID
	if plan.Severity.IsUnknown() {
		plan.Severity = state.Severity
	}
	if _, err := r.readInto(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "Unable to read rule "+state.ID.ValueString(), err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c().Rules.Delete(ctx, &sonarqube.RulesDeleteOptions{Key: state.ID.ValueString()}); err != nil && !isGone(err) {
		apiError(&resp.Diagnostics, "Unable to delete rule "+state.ID.ValueString(), err)
	}
}

func (r *ruleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *ruleResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return upgradeFromJdamata(r)
}
