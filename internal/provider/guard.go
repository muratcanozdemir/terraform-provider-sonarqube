package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// When the provider configuration depends on values known only after apply
// (e.g. host = some_resource.output), Terraform still refreshes existing
// resources and reads data sources during plan, with an unconfigured
// provider. Every resource and data source is therefore wrapped so that no
// method reaches the API client before Configure has supplied one:
//
//   - Read keeps the prior state. The provider is configured again with
//     known values before apply, and the next plan refreshes normally.
//   - Every other operation fails with an explanation instead of crashing.
//
// Plan-time hooks (ModifyPlan) check resourceBase.configured themselves.

const (
	notConfiguredSummary = "SonarQube provider not configured"
	notConfiguredDetail  = "The provider configuration (host or credentials) depends on values that are known only after apply, " +
		"so the SonarQube API cannot be called yet. Apply the resources the provider configuration depends on first " +
		"(for example with -target), or configure the provider from values known at plan time."
)

func addNotConfigured(diags *diag.Diagnostics) {
	diags.AddError(notConfiguredSummary, notConfiguredDetail)
}

// guarded wraps the resources made by newResource.
func guarded(newResource func() resource.Resource) func() resource.Resource {
	return func() resource.Resource { return &guardedResource{inner: newResource()} }
}

type guardedResource struct {
	inner      resource.Resource
	configured bool
}

var (
	_ resource.ResourceWithImportState    = (*guardedResource)(nil)
	_ resource.ResourceWithConfigure      = (*guardedResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*guardedResource)(nil)
	_ resource.ResourceWithValidateConfig = (*guardedResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*guardedResource)(nil)
)

func (r *guardedResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	r.inner.Metadata(ctx, req, resp)
}

func (r *guardedResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.inner.Schema(ctx, req, resp)
}

func (r *guardedResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configured = req.ProviderData != nil
	if c, ok := r.inner.(resource.ResourceWithConfigure); ok {
		c.Configure(ctx, req, resp)
	}
}

func (r *guardedResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	r.inner.Create(ctx, req, resp)
}

func (r *guardedResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured {
		return // resp.State already holds the prior state
	}
	r.inner.Read(ctx, req, resp)
}

func (r *guardedResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	r.inner.Update(ctx, req, resp)
}

func (r *guardedResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	r.inner.Delete(ctx, req, resp)
}

func (r *guardedResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	inner, ok := r.inner.(resource.ResourceWithImportState)
	if !ok {
		resp.Diagnostics.AddError("Import not supported", "This resource cannot be imported.")
		return
	}
	if !r.configured {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	inner.ImportState(ctx, req, resp)
}

func (r *guardedResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if m, ok := r.inner.(resource.ResourceWithModifyPlan); ok {
		m.ModifyPlan(ctx, req, resp)
	}
}

func (r *guardedResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if v, ok := r.inner.(resource.ResourceWithValidateConfig); ok {
		v.ValidateConfig(ctx, req, resp)
	}
}

func (r *guardedResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	if u, ok := r.inner.(resource.ResourceWithUpgradeState); ok {
		return u.UpgradeState(ctx)
	}
	return nil
}

// guardedDataSource wraps the data sources made by newDataSource. A data
// source has no prior state to keep, so its Read fails.
func guardedDataSource(newDataSource func() datasource.DataSource) func() datasource.DataSource {
	return func() datasource.DataSource { return &guardedDS{inner: newDataSource()} }
}

type guardedDS struct {
	inner      datasource.DataSource
	configured bool
}

var (
	_ datasource.DataSourceWithConfigure      = (*guardedDS)(nil)
	_ datasource.DataSourceWithValidateConfig = (*guardedDS)(nil)
)

func (d *guardedDS) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	d.inner.Metadata(ctx, req, resp)
}

func (d *guardedDS) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	d.inner.Schema(ctx, req, resp)
}

func (d *guardedDS) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.configured = req.ProviderData != nil
	if c, ok := d.inner.(datasource.DataSourceWithConfigure); ok {
		c.Configure(ctx, req, resp)
	}
}

func (d *guardedDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.configured {
		addNotConfigured(&resp.Diagnostics)
		return
	}
	d.inner.Read(ctx, req, resp)
}

func (d *guardedDS) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	if v, ok := d.inner.(datasource.DataSourceWithValidateConfig); ok {
		v.ValidateConfig(ctx, req, resp)
	}
}

func guardAll[T any](factories []func() T, guard func(func() T) func() T) []func() T {
	out := make([]func() T, len(factories))
	for i, f := range factories {
		out[i] = guard(f)
	}
	return out
}
