package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestUnconfiguredProvider checks that no registered resource or data source
// reaches the (missing) API client when the provider is not configured, as
// happens when its configuration is unknown during plan.
func TestUnconfiguredProvider(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonarqube"}, &meta)
		t.Run(meta.TypeName, func(t *testing.T) {
			if c, ok := r.(resource.ResourceWithConfigure); ok {
				c.Configure(ctx, resource.ConfigureRequest{}, &resource.ConfigureResponse{})
			}
			var sch resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sch)
			state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}

			read := resource.ReadResponse{State: state}
			r.Read(ctx, resource.ReadRequest{State: state}, &read)
			if read.Diagnostics.HasError() {
				t.Errorf("Read: %v", read.Diagnostics)
			}
			if !read.State.Raw.Equal(state.Raw) {
				t.Error("Read changed the prior state")
			}

			create := resource.CreateResponse{State: state}
			r.Create(ctx, resource.CreateRequest{}, &create)
			if !create.Diagnostics.HasError() {
				t.Error("Create: expected a not-configured error")
			}
			del := resource.DeleteResponse{State: state}
			r.Delete(ctx, resource.DeleteRequest{State: state}, &del)
			if !del.Diagnostics.HasError() {
				t.Error("Delete: expected a not-configured error")
			}
			if im, ok := r.(resource.ResourceWithImportState); ok {
				imp := resource.ImportStateResponse{State: state}
				im.ImportState(ctx, resource.ImportStateRequest{ID: "x"}, &imp)
				if !imp.Diagnostics.HasError() {
					t.Error("ImportState: expected a not-configured error")
				}
			}
		})
	}

	for _, newDataSource := range p.DataSources(ctx) {
		d := newDataSource()
		var meta datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "sonarqube"}, &meta)
		t.Run("data."+meta.TypeName, func(t *testing.T) {
			if c, ok := d.(datasource.DataSourceWithConfigure); ok {
				c.Configure(ctx, datasource.ConfigureRequest{}, &datasource.ConfigureResponse{})
			}
			var resp datasource.ReadResponse
			d.Read(ctx, datasource.ReadRequest{}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Error("Read: expected a not-configured error")
			}
		})
	}
}

// TestAccProvider_unknownConfig plans with a provider configuration that is
// unknown until apply while resources already exist. Their refresh must keep
// the prior state rather than crash, and apply must then proceed normally.
func TestAccProvider_unknownConfig(t *testing.T) {
	g := rname("unk")
	cfg := func(rev int) string {
		return fmt.Sprintf(`
resource "terraform_data" "host" {
  input            = %[1]q
  triggers_replace = %[2]d
}
provider "sonarqube" {
  host = terraform_data.host.output
}
resource "sonarqube_group" "g" {
  name = %[3]q
}
resource "sonarqube_setting" "s" {
  key   = "sonar.dbcleaner.daysBeforeDeletingInactiveBranchesAndPRs"
  value = "31"
}`, os.Getenv("SONAR_HOST"), rev, g)
	}
	tfresource.Test(t, tfresource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []tfresource.TestStep{
			{
				Config: cfg(1),
				Check:  tfresource.TestCheckResourceAttrSet("sonarqube_group.g", "id"),
			},
			{
				// Replacing terraform_data makes the provider host unknown during plan.
				Config: cfg(2),
				ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("terraform_data.host", plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction("sonarqube_group.g", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("sonarqube_setting.s", plancheck.ResourceActionNoop),
				}},
				Check: tfresource.TestCheckResourceAttr("sonarqube_group.g", "name", g),
			},
		},
	})
}
