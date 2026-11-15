package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

func TestAccIdentity(t *testing.T) {
	key, key2 := rname("idp"), rname("idp2")
	profile, profile2 := rname("idqp"), rname("idqp2")
	gate, group, login := rname("idqg"), rname("idgr"), rname("idus")
	cfg := func(key, profile string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project = %[1]q
  name    = "identity"
}
resource "sonarqube_qualityprofile" "qp" {
  name     = %[2]q
  language = "py"
}
resource "sonarqube_qualitygate" "qg" {
  name = %[3]q
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
}
resource "sonarqube_group" "g" {
  name = %[4]q
}
resource "sonarqube_user" "u" {
  login_name = %[5]q
  name       = "identity"
  is_local   = false
}`, key, profile, gate, group, login)
	}
	identities := func(key, profile string) []statecheck.StateCheck {
		return []statecheck.StateCheck{
			statecheck.ExpectIdentity("sonarqube_project.p", map[string]knownvalue.Check{"key": knownvalue.StringExact(key)}),
			statecheck.ExpectIdentity("sonarqube_qualityprofile.qp", map[string]knownvalue.Check{
				"language": knownvalue.StringExact("py"), "name": knownvalue.StringExact(profile)}),
			statecheck.ExpectIdentity("sonarqube_qualitygate.qg", map[string]knownvalue.Check{"name": knownvalue.StringExact(gate)}),
			statecheck.ExpectIdentity("sonarqube_group.g", map[string]knownvalue.Check{"name": knownvalue.StringExact(group)}),
			statecheck.ExpectIdentity("sonarqube_user.u", map[string]knownvalue.Check{"login": knownvalue.StringExact(login)}),
		}
	}
	importStep := func(address string, ignore ...string) resource.TestStep {
		return resource.TestStep{
			ResourceName: address, ImportState: true, ImportStateKind: resource.ImportBlockWithResourceIdentity,
			ImportStateVerifyIgnore: ignore,
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_12_0)},
		Steps: []resource.TestStep{
			{Config: cfg(key, profile), ConfigStateChecks: identities(key, profile)},
			// Key and name changes in place change the identity.
			{Config: cfg(key2, profile2), ConfigStateChecks: identities(key2, profile2)},
			importStep("sonarqube_project.p"),
			importStep("sonarqube_qualityprofile.qp"),
			importStep("sonarqube_qualitygate.qg"),
			importStep("sonarqube_group.g"),
			importStep("sonarqube_user.u"),
		},
	})
}
