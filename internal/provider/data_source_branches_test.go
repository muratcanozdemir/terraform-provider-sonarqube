package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccDataSources_branchesAndGateStatus(t *testing.T) {
	key := rname("dsbr")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project = %[1]q
  name    = %[1]q
}
resource "sonarqube_project_main_branch" "p" {
  project = sonarqube_project.p.project
  name    = "trunk"
}
data "sonarqube_project_branches" "p" {
  project    = sonarqube_project.p.project
  depends_on = [sonarqube_project_main_branch.p]
}
data "sonarqube_quality_gate_status" "p" {
  project = sonarqube_project.p.project
}
data "sonarqube_quality_gate_status" "branch" {
  project    = sonarqube_project.p.project
  branch     = "trunk"
  depends_on = [sonarqube_project_main_branch.p]
}`, key),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "id", key),
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "main_branch", "trunk"),
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "branches.#", "1"),
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "branches.0.name", "trunk"),
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "branches.0.is_main", "true"),
					resource.TestCheckResourceAttr("data.sonarqube_project_branches.p", "branches.0.type", "BRANCH"),
					resource.TestCheckNoResourceAttr("data.sonarqube_project_branches.p", "branches.0.analysis_date"),
					resource.TestCheckResourceAttr("data.sonarqube_quality_gate_status.p", "id", key),
					resource.TestCheckResourceAttr("data.sonarqube_quality_gate_status.p", "status", "NONE"),
					resource.TestCheckResourceAttr("data.sonarqube_quality_gate_status.p", "conditions.#", "0"),
					resource.TestCheckResourceAttr("data.sonarqube_quality_gate_status.branch", "id", key+":trunk"),
					resource.TestCheckResourceAttr("data.sonarqube_quality_gate_status.branch", "status", "NONE"),
				),
			},
			{
				Config: `
data "sonarqube_quality_gate_status" "x" {
  project      = "x"
  branch       = "a"
  pull_request = "1"
}`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				Config: `
data "sonarqube_project_branches" "x" {
  project = "tf-acc-no-such-project"
}`,
				ExpectError: regexp.MustCompile(`Unable to list branches of project`),
			},
		},
	})
}
