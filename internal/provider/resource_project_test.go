package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccProject_lifecycle(t *testing.T) {
	key := rname("proj")
	newKey := key + "-renamed"
	res := "sonarqube_project.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "sonarqube_project" "test" {
  name       = "Acc project"
  project    = %q
  visibility = "private"
  tags       = ["b", "a"]
  setting {
    key   = "sonar.exclusions"
    values = ["**/gen/**", "**/vendor/**"]
  }
  setting {
    key   = "sonar.links.ci"
    value = "https://ci.example.com"
  }
}`, key),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", key),
					resource.TestCheckResourceAttr(res, "visibility", "private"),
					resource.TestCheckResourceAttr(res, "tags.#", "2"),
					resource.TestCheckResourceAttr(res, "setting.#", "2"),
					resource.TestCheckResourceAttr(res, "setting.0.values.#", "2"),
					resource.TestCheckResourceAttrSet(res, "badge_token"),
				),
			},
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"setting"}},
			{
				// Key rename in place, tags cleared, a setting dropped (reset on the server).
				Config: fmt.Sprintf(`
resource "sonarqube_project" "test" {
  name                 = "Acc project"
  project              = %q
  visibility           = "public"
  tags                 = []
  retrieve_badge_token = false
  setting {
    key   = "sonar.links.ci"
    value = "https://ci2.example.com"
  }
}`, newKey),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", newKey),
					resource.TestCheckResourceAttr(res, "visibility", "public"),
					resource.TestCheckResourceAttr(res, "tags.#", "0"),
					resource.TestCheckResourceAttr(res, "setting.#", "1"),
					resource.TestCheckNoResourceAttr(res, "badge_token"),
				),
			},
			{
				// Renaming is refused rather than destroying history.
				Config: fmt.Sprintf(`
resource "sonarqube_project" "test" {
  name    = "Other name"
  project = %q
}`, newKey),
				ExpectError: regexp.MustCompile(`Project name cannot be changed`),
			},
		},
	})
}

// A project deleted outside Terraform is planned for re-creation, not an error.
func TestAccProject_deletedOutOfBand(t *testing.T) {
	key := rname("oob")
	cfg := fmt.Sprintf(`resource "sonarqube_project" "test" {
  name    = "OOB"
  project = %q
}`, key)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					if _, err := testClient(t).Projects.Delete(context.Background(), &sonarqube.ProjectsDeleteOptions{Project: key}); err != nil {
						t.Fatal(err)
					}
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// Creating a project that already exists suggests an import.
func TestAccProject_alreadyExists(t *testing.T) {
	key := rname("dup")
	if _, _, err := testClient(t).Projects.Create(context.Background(), &sonarqube.ProjectsCreateOptions{Project: key, Name: "dup"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testClient(t).Projects.Delete(context.Background(), &sonarqube.ProjectsDeleteOptions{Project: key})
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config:      fmt.Sprintf("resource \"sonarqube_project\" \"test\" {\n  name    = \"dup\"\n  project = %q\n}", key),
			ExpectError: regexp.MustCompile(`terraform import`),
		}},
	})
}
