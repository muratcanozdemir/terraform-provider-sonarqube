package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/querycheck"
	"github.com/hashicorp/terraform-plugin-testing/querycheck/queryfilter"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

func TestAccListResources(t *testing.T) {
	prefix := rname("ls")
	cfg := fmt.Sprintf(`
resource "sonarqube_project" "p" {
  count   = 2
  project = "%[1]s-${count.index}"
  name    = "listed ${count.index}"
}
resource "sonarqube_qualityprofile" "qp" {
  name     = "%[1]s-qp"
  language = "py"
}
resource "sonarqube_group" "g" {
  name = "%[1]s-group"
}
resource "sonarqube_user" "u" {
  login_name = "%[1]s-user"
  name       = "listed"
  is_local   = false
}
resource "sonarqube_qualitygate" "qg" {
  name = "%[1]s-gate"
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
}`, prefix)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				Query: true,
				Config: fmt.Sprintf(`
provider "sonarqube" {}

list "sonarqube_project" "p" {
  provider         = sonarqube
  include_resource = true
  config {
    search = %[1]q
  }
}
list "sonarqube_qualityprofile" "qp" {
  provider = sonarqube
  config {
    language = "py"
  }
}
list "sonarqube_qualityprofile" "builtin" {
  provider = sonarqube
  config {
    language         = "py"
    include_built_in = true
  }
}
list "sonarqube_group" "g" {
  provider = sonarqube
  config {
    search = %[1]q
  }
}
list "sonarqube_user" "u" {
  provider = sonarqube
  config {
    search = %[1]q
  }
}
list "sonarqube_qualitygate" "qg" {
  provider = sonarqube
}
`, prefix),
				QueryResultChecks: []querycheck.QueryResultCheck{
					querycheck.ExpectLength("sonarqube_project.p", 2),
					querycheck.ExpectIdentity("sonarqube_project.p", map[string]knownvalue.Check{"key": knownvalue.StringExact(prefix + "-0")}),
					querycheck.ExpectResourceKnownValues("sonarqube_project.p",
						queryfilter.ByResourceIdentity(map[string]knownvalue.Check{"key": knownvalue.StringExact(prefix + "-1")}),
						[]querycheck.KnownValueCheck{
							{Path: tfjsonpath.New("name"), KnownValue: knownvalue.StringExact("listed 1")},
							{Path: tfjsonpath.New("visibility"), KnownValue: knownvalue.StringExact("public")},
						}),
					querycheck.ExpectIdentity("sonarqube_qualityprofile.qp", map[string]knownvalue.Check{
						"language": knownvalue.StringExact("py"), "name": knownvalue.StringExact(prefix + "-qp")}),
					// Built-in profile names vary by version; there is at least one.
					querycheck.ExpectIdentity("sonarqube_qualityprofile.builtin", map[string]knownvalue.Check{
						"language": knownvalue.StringExact("py"), "name": knownvalue.StringExact(prefix + "-qp")}),
					querycheck.ExpectLengthAtLeast("sonarqube_qualityprofile.builtin", 2),
					querycheck.ExpectLength("sonarqube_group.g", 1),
					querycheck.ExpectIdentity("sonarqube_group.g", map[string]knownvalue.Check{"name": knownvalue.StringExact(prefix + "-group")}),
					querycheck.ExpectLength("sonarqube_user.u", 1),
					querycheck.ExpectIdentity("sonarqube_user.u", map[string]knownvalue.Check{"login": knownvalue.StringExact(prefix + "-user")}),
					querycheck.ExpectIdentity("sonarqube_qualitygate.qg", map[string]knownvalue.Check{"name": knownvalue.StringExact(prefix + "-gate")}),
					querycheck.ExpectNoIdentity("sonarqube_qualitygate.qg", map[string]knownvalue.Check{"name": knownvalue.StringExact("Sonar way")}),
				},
			},
		},
	})
}
