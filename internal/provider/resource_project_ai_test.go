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

func TestAccProject_containsAICode(t *testing.T) {
	requireEdition(t, "developer")
	key := rname("ai")
	cfg := func(ai string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project = %[1]q
  name    = %[1]q
  %[2]s
}`, key, ai)
	}
	check := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			res, _, err := testClient(t).Projects.GetContainsAiCode(context.Background(), &sonarqube.ProjectsGetContainsAiCodeOptions{Project: key})
			if err != nil {
				return err
			}
			if res.ContainsAICode != want {
				return fmt.Errorf("contains_ai_code is %v on the server, want %v", res.ContainsAICode, want)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg("contains_ai_code = true"),
				Check: resource.ComposeAggregateTestCheckFunc(check(true),
					resource.TestCheckResourceAttr("sonarqube_project.p", "contains_ai_code", "true")),
			},
			{
				// Changed outside Terraform: drift, set back.
				PreConfig: func() {
					if err := setContainsAICode(context.Background(), testClient(t), key, false); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("contains_ai_code = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_project.p", plancheck.ResourceActionUpdate),
				}},
				Check: check(true),
			},
			{
				Config: cfg("contains_ai_code = false"),
				Check:  check(false),
			},
			{
				// Omitted: no longer managed, left as it is.
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(check(false),
					resource.TestCheckNoResourceAttr("sonarqube_project.p", "contains_ai_code")),
			},
		},
	})
}

func TestAccProject_containsAICodeCommunityRejected(t *testing.T) {
	if testEdition(t) != "community" {
		t.Skip("community only")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: `
resource "sonarqube_project" "p" {
  project          = "ai-x"
  name             = "ai-x"
  contains_ai_code = true
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`requires SonarQube developer edition`),
		}},
	})
}
