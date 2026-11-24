package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccManagedInstance enables GitHub AUTO_PROVISIONING (with credentials
// that never reach GitHub, so no user is synchronized) over users, a group
// and a membership created before it, and checks what the provider does on
// an externally managed instance: changes SonarQube refuses fail the plan,
// unchanged objects keep planning cleanly, permissions and templates still
// apply, and destroying everything succeeds.
func TestAccManagedInstance(t *testing.T) {
	requireEdition(t, "developer")
	group, login, tpl := rname("mgd"), rname("mgd"), rname("mgd")
	base := fmt.Sprintf(`
resource "sonarqube_group" "g" {
  name = %[1]q
}
resource "sonarqube_user" "u" {
  login_name  = %[2]q
  name        = "Managed probe"
  password_wo = "Acc-Passw0rd-123!"
  password_wo_version = "1"
}
`, group, login)
	member := `
resource "sonarqube_group_member" "m" {
  name       = sonarqube_group.g.name
  login_name = sonarqube_user.u.login_name
}
`
	github := fmt.Sprintf(`
resource "sonarqube_github_configuration" "gh" {
  enabled               = true
  client_id             = "Iv1.acc0000000000000"
  client_secret         = "acc-secret"
  application_id        = "123456"
  private_key           = %q
  allowed_organizations = ["tf-acc-org"]
  provisioning_type     = "AUTO_PROVISIONING"
}
`, testPrivateKey)
	grants := fmt.Sprintf(`
resource "sonarqube_permissions" "g" {
  group_name  = sonarqube_group.g.name
  permissions = ["scan"]
}
resource "sonarqube_permission_template" "t" {
  name = %[1]q
}
resource "sonarqube_permissions" "t" {
  template_id = sonarqube_permission_template.t.id
  group_name  = sonarqube_group.g.name
  permissions = ["codeviewer"]
}
`, tpl)
	refused := regexp.MustCompile(`is not allowed: SonarQube is externally managed`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			// Objects created before provisioning is enabled.
			{Config: base + member},
			// Enabling provisioning leaves them unchanged.
			{
				Config: base + member + github,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_group.g", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("sonarqube_user.u", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("sonarqube_group_member.m", plancheck.ResourceActionNoop),
				}},
			},
			// Now managed: further unchanged plans stay clean.
			{Config: base + member + github, PlanOnly: true},
			// Changes SonarQube would refuse fail at plan time.
			{Config: base + member + github + `
resource "sonarqube_group" "new" {
  name = "tf-acc-mgd-refused"
}`, ExpectError: refused, PlanOnly: true},
			{Config: base + member + github + `
resource "sonarqube_user" "new" {
  login_name = "tf-acc-mgd-refused"
  name       = "Refused"
  is_local   = false
}`, ExpectError: refused, PlanOnly: true},
			{Config: regexp.MustCompile(`name        = "Managed probe"`).ReplaceAllString(base, `name        = "Renamed"`) + member + github,
				ExpectError: refused, PlanOnly: true},
			// A password change is still allowed.
			{Config: regexp.MustCompile(`password_wo_version = "1"`).ReplaceAllString(base, `password_wo_version = "2"`) + member + github},
			// Permissions and templates are unaffected.
			{Config: regexp.MustCompile(`password_wo_version = "1"`).ReplaceAllString(base, `password_wo_version = "2"`) + member + github + grants},
			// Dropping the membership cannot remove it in SonarQube: it is
			// forgotten with a warning instead of failing the apply.
			{Config: regexp.MustCompile(`password_wo_version = "1"`).ReplaceAllString(base, `password_wo_version = "2"`) + github + grants},
		},
	})
}
