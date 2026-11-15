package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// Sub-portfolios and the branches of applications in a portfolio.
func TestAccPortfolio_hierarchy(t *testing.T) {
	requireEdition(t, "enterprise")
	p, app, s1, s2, pf := rname("php"), rname("pha"), rname("phs1"), rname("phs2"), rname("phpf")
	cfg := func(sub, branches string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "ph"
  project = %[1]q
}
resource "sonarqube_application" "a" {
  key      = %[2]q
  name     = "PH app"
  projects = [sonarqube_project.p.project]
}
resource "sonarqube_application_branch" "release" {
  application = sonarqube_application.a.key
  name        = "release"
  projects    = { (sonarqube_project.p.project) = "" }
}
resource "sonarqube_portfolio" "s1" {
  key  = %[3]q
  name = "Sub 1"
}
resource "sonarqube_portfolio" "s2" {
  key  = %[4]q
  name = "Sub 2"
}
resource "sonarqube_portfolio" "pf" {
  key                  = %[5]q
  name                 = "Parent"
  portfolios           = [%[6]s]
  applications         = [sonarqube_application.a.key]
  application_branches = { (sonarqube_application.a.key) = %[7]s }
  depends_on           = [sonarqube_application_branch.release]
}`, p, app, s1, s2, pf, sub, branches)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("sonarqube_portfolio.s1.key", `[]`), PlanOnly: true, ExpectError: regexp.MustCompile(`at\s+least\s+1`)},
			{
				Config: fmt.Sprintf(`
resource "sonarqube_portfolio" "pf" {
  key                  = %q
  name                 = "Parent"
  applications         = ["a"]
  application_branches = { b = ["main"] }
}`, pf),
				PlanOnly: true, ExpectError: regexp.MustCompile(`not\s+in\s+applications`),
			},
			{
				Config: cfg("sonarqube_portfolio.s1.key", `["main", "release"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "portfolios.#", "1"),
					resource.TestCheckTypeSetElemAttr("sonarqube_portfolio.pf", "portfolios.*", s1),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "application_branches."+app+".#", "2"),
				),
			},
			// Swapping the sub-portfolio, and dropping the main branch, which must
			// not take the application out of the portfolio.
			{
				Config: cfg("sonarqube_portfolio.s2.key", `["release"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_portfolio.pf", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemAttr("sonarqube_portfolio.pf", "portfolios.*", s2),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "portfolios.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "applications.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "application_branches."+app+".#", "1"),
					resource.TestCheckTypeSetElemAttr("sonarqube_portfolio.pf", "application_branches."+app+".*", "release"),
				),
			},
			// Imports do not manage branch selections.
			{ResourceName: "sonarqube_portfolio.pf", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"application_branches"}},
		},
	})
}
