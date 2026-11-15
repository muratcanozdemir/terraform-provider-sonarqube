package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

const testPrivateKey = `-----BEGIN RSA PRIVATE KEY-----
MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
KUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQJAIJLixBy2qpFoS4DSmoEm
-----END RSA PRIVATE KEY-----`

func TestAccAlmSettings(t *testing.T) {
	gh, gl, az, bb, bbc := rname("gh"), rname("gl"), rname("az"), rname("bb"), rname("bbc")
	// Below Enterprise, Bitbucket Server and Cloud share the single Bitbucket slot.
	bbCloud := editionRank[testEdition(t)] >= editionRank["enterprise"]
	cfg := func(secret, glURL string) string {
		return fmt.Sprintf(`
resource "sonarqube_alm_github" "gh" {
  key           = %[1]q
  url           = "https://api.github.com/"
  app_id        = "12345"
  client_id     = "Iv1.abc"
  client_secret = %[6]q
  private_key   = %[7]q
}
resource "sonarqube_alm_gitlab" "gl" {
  key                   = %[2]q
  url                   = %[8]q
  personal_access_token = "glpat-x"
}
resource "sonarqube_alm_azure" "az" {
  key                   = %[3]q
  url                   = "https://dev.azure.com/acme"
  personal_access_token = "az-pat"
}
resource "sonarqube_alm_bitbucket" "bb" {
  key                   = %[4]q
  url                   = "https://bitbucket.example.com"
  personal_access_token = "bb-pat"
}
%[9]s`, gh, gl, az, bb, bbc, secret, testPrivateKey, glURL, map[bool]string{true: fmt.Sprintf(`
resource "sonarqube_alm_bitbucket_cloud" "bbc" {
  key           = %q
  workspace     = "acme"
  client_id     = "consumer"
  client_secret = "consumer-secret"
}`, bbc)}[bbCloud])
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("s1", "https://gitlab.example.com/api/v4")},
			{ResourceName: "sonarqube_alm_github.gh", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"client_secret", "private_key", "url"}},
			{ResourceName: "sonarqube_alm_gitlab.gl", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"personal_access_token"}},
			{
				// Rotating a secret or moving the URL updates in place (upstream replaced the setting, dropping bindings).
				Config: cfg("s2", "https://gitlab2.example.com/api/v4"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_alm_github.gh", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("sonarqube_alm_gitlab.gl", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("sonarqube_alm_gitlab.gl", "url", "https://gitlab2.example.com/api/v4"),
			},
		},
	})
}

func TestAccBindings(t *testing.T) {
	requireEdition(t, "developer")
	p1, p2, gl, az := rname("b1"), rname("b2"), rname("gl"), rname("az")
	cfg := func(repo string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p1" {
  name    = "b1"
  project = %[1]q
}
resource "sonarqube_project" "p2" {
  name    = "b2"
  project = %[2]q
}
resource "sonarqube_alm_gitlab" "gl" {
  key                   = %[3]q
  url                   = "https://gitlab.example.com/api/v4"
  personal_access_token = "glpat-x"
}
resource "sonarqube_alm_azure" "az" {
  key                   = %[4]q
  url                   = "https://dev.azure.com/acme"
  personal_access_token = "az-pat"
}
resource "sonarqube_gitlab_binding" "b" {
  project     = sonarqube_project.p1.project
  alm_setting = sonarqube_alm_gitlab.gl.key
  repository  = %[5]q
}
resource "sonarqube_azure_binding" "b" {
  project         = sonarqube_project.p2.project
  alm_setting     = sonarqube_alm_azure.az.key
  project_name    = "AcmeProject"
  repository_name = "acme-repo"
  monorepo        = true
}`, p1, p2, gl, az, repo)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("123")},
			{ResourceName: "sonarqube_gitlab_binding.b", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_azure_binding.b", ImportState: true, ImportStateVerify: true},
			{
				Config: cfg("456"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_gitlab_binding.b", plancheck.ResourceActionUpdate),
				}},
			},
		},
	})
}

// A setting with bindings outside Terraform must not be deleted.
func TestAccAlmSetting_refusesDeleteWhenBound(t *testing.T) {
	requireEdition(t, "developer")
	p, gl := rname("rb"), rname("gl")
	project := fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "rb"
  project = %q
}`, p)
	alm := fmt.Sprintf(`
resource "sonarqube_alm_gitlab" "gl" {
  key                   = %q
  url                   = "https://gitlab.example.com/api/v4"
  personal_access_token = "glpat-x"
}`, gl)
	binding := `
resource "sonarqube_gitlab_binding" "b" {
  project     = sonarqube_project.p.project
  alm_setting = sonarqube_alm_gitlab.gl.key
  repository  = "1"
}`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: project + alm + binding},
			// Forget the binding (as if bound outside Terraform), then try to delete the setting.
			{Config: project + alm + `
removed {
  from = sonarqube_gitlab_binding.b
  lifecycle { destroy = false }
}`},
			{Config: project, ExpectError: regexp.MustCompile(`DevOps platform setting is in use`)},
			{Config: project + alm + `
import {
  to = sonarqube_gitlab_binding.b
  id = "` + p + `"
}` + binding},
		},
	})
}

// Bindings are rejected at plan time on Community Build.
func TestAccBinding_communityRejectedAtPlan(t *testing.T) {
	if testEdition(t) != "community" {
		t.Skip("community only")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: `
resource "sonarqube_gitlab_binding" "b" {
  project     = "x"
  alm_setting = "y"
  repository  = "1"
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`requires SonarQube developer edition`),
		}},
	})
}

// Write-only credentials are never stored, and are sent again from the
// configuration when another attribute changes.
func TestAccAlmGithub_writeOnly(t *testing.T) {
	key := rname("ghwo")
	cfg := func(appID, version string) string {
		return fmt.Sprintf(`
resource "sonarqube_alm_github" "g" {
  key                    = %q
  url                    = "https://api.github.com/"
  app_id                 = %q
  client_id              = "Iv1.wo"
  client_secret_wo       = "secret-%[3]s"
  private_key_wo         = %q
  credentials_wo_version = %q
}`, key, appID, version, testPrivateKey, version)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			// A value and its write-only form conflict.
			{
				Config: fmt.Sprintf(`
resource "sonarqube_alm_github" "g" {
  key              = %q
  url              = "https://api.github.com/"
  app_id           = "2"
  client_id        = "Iv1.wo"
  client_secret    = "plain"
  client_secret_wo = "wo"
  private_key      = "k"
}`, key),
				ExpectError: regexp.MustCompile(`(?s)client_secret.*client_secret_wo`),
			},
			{Config: cfg("1", "1"), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("sonarqube_alm_github.g", "client_secret"),
				resource.TestCheckNoResourceAttr("sonarqube_alm_github.g", "client_secret_wo"),
				resource.TestCheckNoResourceAttr("sonarqube_alm_github.g", "private_key_wo"),
			)},
			// SonarQube needs the secrets again on update; they come from the configuration.
			{Config: cfg("2", "1"), Check: resource.TestCheckResourceAttr("sonarqube_alm_github.g", "app_id", "2")},
			{Config: cfg("2", "2"), ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("sonarqube_alm_github.g", plancheck.ResourceActionUpdate)},
			}},
		},
	})
}

// Personal access tokens and the remaining secrets have write-only forms too:
// none of them reaches state, and bumping the version sends them again.
func TestAccAlmTokens_writeOnly(t *testing.T) {
	gl, az, bb, bbc, gh := rname("glwo"), rname("azwo"), rname("bbwo"), rname("bbcwo"), rname("ghwh")
	bbCloud := editionRank[testEdition(t)] >= editionRank["enterprise"]
	cfg := func(version string) string {
		c := fmt.Sprintf(`
resource "sonarqube_alm_gitlab" "gl" {
  key                      = %[1]q
  url                      = "https://gitlab.example.com/api/v4"
  personal_access_token_wo = "glpat-%[6]s"
  credentials_wo_version   = %[6]q
}
resource "sonarqube_alm_azure" "az" {
  key                      = %[2]q
  url                      = "https://dev.azure.com/acme"
  personal_access_token_wo = "az-%[6]s"
  credentials_wo_version   = %[6]q
}
resource "sonarqube_alm_github" "gh" {
  key                    = %[5]q
  url                    = "https://api.github.com/"
  app_id                 = "3"
  client_id              = "Iv1.wh"
  client_secret_wo       = "secret-%[6]s"
  private_key_wo         = %[7]q
  webhook_secret_wo      = "hook-%[6]s"
  credentials_wo_version = %[6]q
}
`, gl, az, bb, bbc, gh, version, testPrivateKey)
		// Below Enterprise, Bitbucket Server and Cloud share the single Bitbucket slot.
		if bbCloud {
			c += fmt.Sprintf(`
resource "sonarqube_alm_bitbucket_cloud" "bbc" {
  key                    = %q
  workspace              = "acme"
  client_id              = "consumer"
  client_secret_wo       = "consumer-%s"
  credentials_wo_version = %q
}`, bbc, version, version)
		} else {
			c += fmt.Sprintf(`
resource "sonarqube_alm_bitbucket" "bb" {
  key                      = %q
  url                      = "https://bitbucket.example.com"
  personal_access_token_wo = "bb-%s"
  credentials_wo_version   = %q
}`, bb, version, version)
		}
		return c
	}
	bbName, bbAttr := "sonarqube_alm_bitbucket.bb", "personal_access_token"
	if bbCloud {
		bbName, bbAttr = "sonarqube_alm_bitbucket_cloud.bbc", "client_secret"
	}
	noSecrets := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckNoResourceAttr("sonarqube_alm_gitlab.gl", "personal_access_token"),
		resource.TestCheckNoResourceAttr("sonarqube_alm_gitlab.gl", "personal_access_token_wo"),
		resource.TestCheckNoResourceAttr("sonarqube_alm_azure.az", "personal_access_token"),
		resource.TestCheckNoResourceAttr("sonarqube_alm_azure.az", "personal_access_token_wo"),
		resource.TestCheckNoResourceAttr("sonarqube_alm_github.gh", "webhook_secret"),
		resource.TestCheckNoResourceAttr("sonarqube_alm_github.gh", "webhook_secret_wo"),
		resource.TestCheckNoResourceAttr(bbName, bbAttr),
		resource.TestCheckNoResourceAttr(bbName, bbAttr+"_wo"),
	)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			// A write-only token needs credentials_wo_version.
			{
				Config: fmt.Sprintf(`
resource "sonarqube_alm_gitlab" "gl" {
  key                      = %q
  url                      = "https://gitlab.example.com/api/v4"
  personal_access_token_wo = "glpat-x"
}`, gl),
				ExpectError: regexp.MustCompile(`credentials_wo_version`),
			},
			// A required token must be given in one of its two forms.
			{
				Config: fmt.Sprintf(`
resource "sonarqube_alm_azure" "az" {
  key = %q
  url = "https://dev.azure.com/acme"
}`, az),
				ExpectError: regexp.MustCompile(`(?s)personal_access_token.*personal_access_token_wo`),
			},
			{Config: cfg("1"), Check: noSecrets},
			{ResourceName: "sonarqube_alm_gitlab.gl", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"credentials_wo_version"}},
			{Config: cfg("2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("sonarqube_alm_gitlab.gl", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("sonarqube_alm_azure.az", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("sonarqube_alm_github.gh", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction(bbName, plancheck.ResourceActionUpdate),
			}}, Check: noSecrets},
		},
	})
}
