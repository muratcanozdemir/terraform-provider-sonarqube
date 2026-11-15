package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccApplicationBranch(t *testing.T) {
	requireEdition(t, "developer")
	p1, p2, app := rname("abp1"), rname("abp2"), rname("aba")
	cfg := func(name, projects string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p1" {
  name    = "ab1"
  project = %[1]q
}
resource "sonarqube_project" "p2" {
  name    = "ab2"
  project = %[2]q
}
# p2's main branch is "trunk", so "" and "trunk" name the same branch.
resource "sonarqube_project_main_branch" "p2" {
  project = sonarqube_project.p2.project
  name    = "trunk"
}
resource "sonarqube_application" "a" {
  key      = %[3]q
  name     = "AB"
  projects = [sonarqube_project.p1.project, sonarqube_project.p2.project]
}
resource "sonarqube_application_branch" "b" {
  application = sonarqube_application.a.key
  name        = %[4]q
  projects    = %[5]s
  depends_on  = [sonarqube_project_main_branch.p2]
}`, p1, p2, app, name, projects)
	}
	both := `{ (sonarqube_project.p1.project) = "", (sonarqube_project.p2.project) = "trunk" }`
	onlyP1 := `{ (sonarqube_project.p1.project) = "" }`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg("release", both),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "id", app+"/release"),
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "projects.%", "2"),
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "projects."+p1, ""),
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "projects."+p2, "trunk"),
				),
			},
			// Renaming and changing the selection is one in-place update.
			{
				Config: cfg("release-2", onlyP1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_application_branch.b", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "id", app+"/release-2"),
					resource.TestCheckResourceAttr("sonarqube_application_branch.b", "projects.%", "1"),
				),
			},
			// An import has no configuration to tell "" from the main branch's name.
			{
				ResourceName: "sonarqube_application_branch.b", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"projects." + p1},
				ImportStateCheck: func(s []*terraform.InstanceState) error {
					if got := s[0].Attributes["projects."+p1]; got != "main" {
						return fmt.Errorf("imported branch of %s = %q, want main", p1, got)
					}
					return nil
				},
			},
			// Deleted outside Terraform: planned for creation again.
			{
				PreConfig: func() {
					if _, err := testClient(t).Applications.DeleteBranch(context.Background(),
						&sonarqube.ApplicationsDeleteBranchOptions{Application: app, Branch: "release-2"}); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("release-2", onlyP1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_application_branch.b", plancheck.ResourceActionCreate),
				}},
			},
			{
				Config:      cfg("release-3", `{ (sonarqube_project.p1.project) = "no-such-branch" }`),
				ExpectError: regexp.MustCompile(`not valid`),
			},
		},
	})
}

func TestAccApplicationBranch_communityRejectedAtPlan(t *testing.T) {
	if testEdition(t) != "community" {
		t.Skip("community only")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: `
resource "sonarqube_application_branch" "b" {
  application = "x"
  name        = "release"
  projects    = { x = "" }
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`requires SonarQube developer edition`),
		}},
	})
}
