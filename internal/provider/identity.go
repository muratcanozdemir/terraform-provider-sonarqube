package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Resource identity (Terraform 1.12+) names the SonarQube object a resource
// manages with typed attributes instead of one import ID string. It lets
// import blocks use `identity = { ... }` and is what list resources return.
//
// The identity of every resource here consists of state attributes that
// already make up its import ID, so it is added by a wrapper: after Create,
// Read and Update the identity is copied from the new state, and an import
// by identity is turned into the import ID and handled by the resource's own
// ImportState.

// identityAttr maps an identity attribute to the state attribute it copies.
type identityAttr struct {
	name, state, description string
}

type identitySpec struct {
	attrs []identityAttr
	// importID joins the identity values, in attrs order, into the
	// resource's import ID. Nil means the single value is the import ID.
	importID func(values []string) string
	// mutable is set when an identity attribute can change in place, e.g.
	// a project key or a profile name.
	mutable bool
}

func (s identitySpec) id(values []string) string {
	if s.importID == nil {
		return values[0]
	}
	return s.importID(values)
}

// identityOf reads the identity values from a resource's state. ok is false
// when a value is null or unknown.
func (s identitySpec) identityOf(ctx context.Context, state tfsdk.State) (values []string, ok bool, diags diag.Diagnostics) {
	for _, a := range s.attrs {
		var v types.String
		diags.Append(state.GetAttribute(ctx, path.Root(a.state), &v)...)
		if diags.HasError() || v.IsNull() || v.IsUnknown() {
			return nil, false, diags
		}
		values = append(values, v.ValueString())
	}
	return values, true, diags
}

func (s identitySpec) setIdentity(ctx context.Context, state tfsdk.State, identity *tfsdk.ResourceIdentity) diag.Diagnostics {
	if identity == nil || state.Raw.IsNull() {
		return nil
	}
	values, ok, diags := s.identityOf(ctx, state)
	if !ok {
		return diags
	}
	for i, a := range s.attrs {
		diags.Append(identity.SetAttribute(ctx, path.Root(a.name), values[i])...)
	}
	return diags
}

// withIdentity adds resource identity to the resources made by newResource.
func withIdentity(newResource func() resource.Resource, spec identitySpec) func() resource.Resource {
	return func() resource.Resource { return &identityResource{inner: newResource(), spec: spec} }
}

type identityResource struct {
	inner resource.Resource
	spec  identitySpec
}

var (
	_ resource.ResourceWithIdentity       = (*identityResource)(nil)
	_ resource.ResourceWithImportState    = (*identityResource)(nil)
	_ resource.ResourceWithConfigure      = (*identityResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*identityResource)(nil)
	_ resource.ResourceWithValidateConfig = (*identityResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*identityResource)(nil)
)

func (r *identityResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	r.inner.Metadata(ctx, req, resp)
	resp.ResourceBehavior.MutableIdentity = r.spec.mutable
}

func (r *identityResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.inner.Schema(ctx, req, resp)
}

func (r *identityResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	attrs := map[string]identityschema.Attribute{}
	for _, a := range r.spec.attrs {
		attrs[a.name] = identityschema.StringAttribute{RequiredForImport: true, Description: a.description}
	}
	resp.IdentitySchema = identityschema.Schema{Attributes: attrs}
}

func (r *identityResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := r.inner.(resource.ResourceWithConfigure); ok {
		c.Configure(ctx, req, resp)
	}
}

func (r *identityResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.inner.Create(ctx, req, resp)
	resp.Diagnostics.Append(r.spec.setIdentity(ctx, resp.State, resp.Identity)...)
}

func (r *identityResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	r.inner.Read(ctx, req, resp)
	state := resp.State
	if state.Raw.IsNull() {
		// Removed from state (deleted outside Terraform). The framework still
		// requires an identity, and the stored one is null when the state
		// predates identity (Terraform before 1.12, or state written by an
		// earlier provider version, e.g. migrated from jdamata), so describe
		// the object that was removed.
		state = req.State
	}
	resp.Diagnostics.Append(r.spec.setIdentity(ctx, state, resp.Identity)...)
}

func (r *identityResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.inner.Update(ctx, req, resp)
	resp.Diagnostics.Append(r.spec.setIdentity(ctx, resp.State, resp.Identity)...)
}

func (r *identityResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	r.inner.Delete(ctx, req, resp)
}

func (r *identityResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	inner, ok := r.inner.(resource.ResourceWithImportState)
	if !ok {
		resp.Diagnostics.AddError("Import not supported", "This resource cannot be imported.")
		return
	}
	if req.ID == "" && req.Identity != nil {
		var values []string
		for _, a := range r.spec.attrs {
			var v types.String
			resp.Diagnostics.Append(req.Identity.GetAttribute(ctx, path.Root(a.name), &v)...)
			if v.IsNull() || v.ValueString() == "" {
				resp.Diagnostics.AddAttributeError(path.Root(a.name), "Missing identity attribute", fmt.Sprintf("%s is required to import by identity.", a.name))
			}
			values = append(values, v.ValueString())
		}
		if resp.Diagnostics.HasError() {
			return
		}
		req.ID = r.spec.id(values)
	}
	inner.ImportState(ctx, req, resp)
}

func (r *identityResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if m, ok := r.inner.(resource.ResourceWithModifyPlan); ok {
		m.ModifyPlan(ctx, req, resp)
	}
}

func (r *identityResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if v, ok := r.inner.(resource.ResourceWithValidateConfig); ok {
		v.ValidateConfig(ctx, req, resp)
	}
}

func (r *identityResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	if u, ok := r.inner.(resource.ResourceWithUpgradeState); ok {
		return u.UpgradeState(ctx)
	}
	return nil
}

// Identity specs of the resources that have one.
var (
	keyIdentity = func(what string) identitySpec {
		return identitySpec{attrs: []identityAttr{{"key", "key", what + " key."}}, mutable: true}
	}
	fixedKeyIdentity = func(what string) identitySpec {
		return identitySpec{attrs: []identityAttr{{"key", "key", what + " key."}}}
	}
	nameIdentity = func(what string) identitySpec {
		return identitySpec{attrs: []identityAttr{{"name", "name", what + " name."}}, mutable: true}
	}
	slashIdentity = func(values []string) string { return strings.Join(values, "/") }

	projectIdentity = identitySpec{attrs: []identityAttr{{"key", "project", "Project key."}}, mutable: true}
	userIdentity    = identitySpec{attrs: []identityAttr{{"login", "login_name", "User login."}}, mutable: true}
	profileIdentity = identitySpec{attrs: []identityAttr{
		{"language", "language", "Language key."}, {"name", "name", "Profile name."}}, importID: slashIdentity, mutable: true}
	appBranchIdentity = identitySpec{attrs: []identityAttr{
		{"application", "application", "Application key."}, {"name", "name", "Branch name."}}, importID: slashIdentity, mutable: true}
	emailIdentity = identitySpec{attrs: []identityAttr{{"id", "id", "`email-configuration`."}}}
)
