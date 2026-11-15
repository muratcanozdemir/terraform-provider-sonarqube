package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func TestAccApplicationAndPortfolio(t *testing.T) {
	requireEdition(t, "enterprise")
	p1, p2, app, pf := rname("va"), rname("vb"), rname("app"), rname("pf")
	cfg := func(appProjects, appTags, pfMode string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p1" {
  name    = "va"
  project = %[1]q
  tags    = ["team-a"]
}
resource "sonarqube_project" "p2" {
  name    = "vb"
  project = %[2]q
}
resource "sonarqube_application" "a" {
  key         = %[3]q
  name        = "App"
  description = "apps"
  projects    = %[5]s
  tags        = %[6]s
}
resource "sonarqube_portfolio" "pf" {
  key          = %[4]q
  name         = "PF"
  description  = "portfolio"
  applications = [sonarqube_application.a.key]
  %[7]s
}`, p1, p2, app, pf, appProjects, appTags, pfMode)
	}
	manual := fmt.Sprintf(`selection_mode = "MANUAL"
  selected_projects = [{ project_key = %q }]`, p1)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg("[sonarqube_project.p1.project, sonarqube_project.p2.project]", `["x", "y"]`, manual),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_application.a", "projects.#", "2"),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "selected_projects.#", "1"),
					resource.TestCheckNoResourceAttr("sonarqube_portfolio.pf", "selected_projects.0.selected_branches"), // main branch implied
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "applications.#", "1"),
				),
			},
			{ResourceName: "sonarqube_application.a", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_portfolio.pf", ImportState: true, ImportStateVerify: true},
			{
				Config: cfg("[sonarqube_project.p1.project]", `[]`, `selection_mode = "TAGS"
  tags = ["team-a"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_application.a", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("sonarqube_portfolio.pf", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_application.a", "projects.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_application.a", "tags.#", "0"),
					resource.TestCheckResourceAttr("sonarqube_portfolio.pf", "selection_mode", "TAGS"),
				),
			},
			{Config: cfg("[sonarqube_project.p1.project]", `[]`, `selection_mode = "REGEXP"
  regexp = "tf-acc-.*"`)},
		},
	})
}

func TestAccPortfolio_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: `
resource "sonarqube_portfolio" "pf" {
  key            = "x"
  name           = "x"
  selection_mode = "REGEXP"
  tags           = ["a"]
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`tags only applies to selection_mode`),
		}},
	})
}
