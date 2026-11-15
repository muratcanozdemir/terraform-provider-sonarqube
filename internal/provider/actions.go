package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// Actions (Terraform 1.14+) are operations without state: run with
// `terraform apply -invoke=action.<type>.<name>`, or from a resource's
// lifecycle `action_trigger`.

func (p *sonarqubeProvider) Actions(context.Context) []func() action.Action {
	return []func() action.Action{
		func() action.Action { return &sendTestEmailAction{} },
		func() action.Action { return &applyPermissionTemplateAction{} },
	}
}

type actionBase struct{ p *providerData }

func (a *actionBase) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	p, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *providerData, got %T.", req.ProviderData))
		return
	}
	a.p = p
}

// ---------------------------------------------------------------------------
// action sonarqube_send_test_email

type sendTestEmailAction struct{ actionBase }

var _ action.ActionWithConfigure = (*sendTestEmailAction)(nil)

type sendTestEmailModel struct {
	To      types.String `tfsdk:"to"`
	Subject types.String `tfsdk:"subject"`
	Message types.String `tfsdk:"message"`
}

func (a *sendTestEmailAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_send_test_email"
}

func (a *sendTestEmailAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sends a test email through the server's email configuration, e.g. after creating or changing " +
			"`sonarqube_email_configuration`, so that a wrong SMTP setting fails the apply instead of going unnoticed.",
		Attributes: map[string]schema.Attribute{
			"to": schema.StringAttribute{Required: true, Description: "Recipient address.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(3)}},
			"subject": schema.StringAttribute{Optional: true, Description: "Subject. Defaults to `Test message from SonarQube`."},
			"message": schema.StringAttribute{Optional: true, Description: "Body. Defaults to a line saying it was sent by Terraform."},
		},
	}
}

func (a *sendTestEmailAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	if a.p == nil {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	var m sendTestEmailModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := a.p.client
	// Without a configuration the server answers a bare HTTP 500.
	if cfgs, _, err := c.SystemV2.SearchEmailConfigurations(ctx); err == nil && len(cfgs.EmailConfigurations) == 0 {
		resp.Diagnostics.AddError("No email configuration", "The server has no email configuration to send through. "+
			"Create one with sonarqube_email_configuration first, and trigger this action after it.")
		return
	}
	subject := m.Subject.ValueString()
	if subject == "" {
		subject = "Test message from SonarQube"
	}
	message := m.Message.ValueString()
	if message == "" {
		message = "This is a test message from SonarQube, sent by Terraform to check the email configuration."
	}
	if _, err := c.Emails.Send(ctx, &sonarqube.EmailsSendOptions{To: m.To.ValueString(), Subject: subject, Message: message}); err != nil {
		apiError(&resp.Diagnostics, "Unable to send a test email to "+m.To.ValueString(), err)
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "Sent a test email to " + m.To.ValueString()})
	}
}

// ---------------------------------------------------------------------------
// action sonarqube_apply_permission_template

type applyPermissionTemplateAction struct{ actionBase }

var (
	_ action.ActionWithConfigure      = (*applyPermissionTemplateAction)(nil)
	_ action.ActionWithValidateConfig = (*applyPermissionTemplateAction)(nil)
)

type applyPermissionTemplateModel struct {
	Template    types.String `tfsdk:"template"`
	Projects    types.Set    `tfsdk:"projects"`
	Search      types.String `tfsdk:"search"`
	AllProjects types.Bool   `tfsdk:"all_projects"`
}

func (a *applyPermissionTemplateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_apply_permission_template"
}

func (a *applyPermissionTemplateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Applies a permission template to existing projects, replacing their permissions with the template's. " +
			"SonarQube applies templates only when a project is created, so changes to a template (e.g. a " +
			"`sonarqube_permission_template` or its permissions) do not reach existing projects without this action. " +
			"Choose the projects with exactly one of `projects`, `search` or `all_projects`. Do not apply a template to " +
			"projects whose permissions `sonarqube_permissions` manages: the next plan would undo it.",
		Attributes: map[string]schema.Attribute{
			"template": schema.StringAttribute{Required: true, Description: "Permission template name."},
			"projects": schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Keys of the projects.",
				Validators: []validator.Set{setvalidator.SizeAtLeast(1)}},
			"search": schema.StringAttribute{Optional: true,
				Description: "Projects whose name contains this text, or whose key is exactly this text.",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1)}},
			"all_projects": schema.BoolAttribute{Optional: true, Description: "Set to `true` to apply the template to every project."},
		},
	}
}

func (a *applyPermissionTemplateAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m applyPermissionTemplateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Projects.IsUnknown() || m.Search.IsUnknown() || m.AllProjects.IsUnknown() {
		return
	}
	n := 0
	for _, set := range []bool{!m.Projects.IsNull(), !m.Search.IsNull(), m.AllProjects.ValueBool()} {
		if set {
			n++
		}
	}
	if n != 1 {
		resp.Diagnostics.AddAttributeError(path.Root("projects"), "Choose the projects",
			"Set exactly one of projects, search or all_projects = true.")
	}
}

func (a *applyPermissionTemplateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	if a.p == nil {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	var m applyPermissionTemplateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tpl := m.Template.ValueString()
	opts := &sonarqube.PermissionsBulkApplyTemplateOptions{TemplateName: tpl, Q: m.Search.ValueString(), Qualifiers: []string{"TRK"}}
	what := "every project"
	switch {
	case !m.Projects.IsNull():
		opts.Projects = stringsOf(ctx, m.Projects, &resp.Diagnostics)
		what = strings.Join(opts.Projects, ", ")
	case !m.Search.IsNull():
		what = fmt.Sprintf("projects matching %q", m.Search.ValueString())
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := a.p.client.Permissions.BulkApplyTemplate(ctx, opts); err != nil {
		apiError(&resp.Diagnostics, "Unable to apply permission template "+tpl, err)
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: fmt.Sprintf("Applied permission template %q to %s", tpl, what)})
	}
}
