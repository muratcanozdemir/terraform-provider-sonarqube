package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// The configurations stay disabled: they are stored and validated, but
// nothing logs in through them, so no real GitHub App or GitLab application
// is needed.

func TestAccGithubConfiguration(t *testing.T) {
	key := testPrivateKey
	cfg := func(extra string) string {
		return fmt.Sprintf(`
resource "sonarqube_github_configuration" "g" {
  enabled        = false
  client_id      = "Iv1.acc0000000000000"
  client_secret  = "acc-secret"
  application_id = "123456"
  private_key    = %q
  %s
}`, key, extra)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "id", "github-configuration"),
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "api_url", "https://api.github.com/"),
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "provisioning_type", "JIT"),
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "allowed_organizations.#", "0"),
				),
			},
			{ResourceName: "sonarqube_github_configuration.g", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"client_id", "client_secret", "private_key"}},
			{
				Config: cfg(`allowed_organizations = ["acc-org-a", "acc-org-b"]
  synchronize_groups    = true
  web_url               = "https://github.example.com/"
  api_url               = "https://github.example.com/api/v3/"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_github_configuration.g", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "allowed_organizations.#", "2"),
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "synchronize_groups", "true"),
					resource.TestCheckResourceAttr("sonarqube_github_configuration.g", "web_url", "https://github.example.com/"),
				),
			},
			{
				// SonarQube allows one configuration: a second one suggests an import.
				Config: cfg("") + `
resource "sonarqube_github_configuration" "dup" {
  enabled        = false
  client_id      = "x"
  client_secret  = "x"
  application_id = "1"
  private_key    = sonarqube_github_configuration.g.private_key
}`,
				ExpectError: regexp.MustCompile(`terraform import`),
			},
		},
	})
}

func TestAccGithubConfiguration_writeOnly(t *testing.T) {
	key := testPrivateKey
	cfg := func(version string) string {
		return fmt.Sprintf(`
resource "sonarqube_github_configuration" "g" {
  enabled                = false
  client_id              = "Iv1.acc0000000000000"
  client_secret_wo       = "acc-secret-%[2]s"
  application_id         = "123456"
  private_key_wo         = %[1]q
  credentials_wo_version = %[2]q
}`, key, version)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: cfg("1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("sonarqube_github_configuration.g", "client_secret_wo"),
					resource.TestCheckNoResourceAttr("sonarqube_github_configuration.g", "private_key_wo"),
				),
			},
			{
				Config: cfg("2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_github_configuration.g", plancheck.ResourceActionUpdate),
				}},
			},
		},
	})
}

func TestAccGitlabConfiguration(t *testing.T) {
	cfg := func(extra string) string {
		return fmt.Sprintf(`
resource "sonarqube_gitlab_configuration" "g" {
  enabled        = false
  application_id = "acc-app"
  secret         = "acc-secret"
  %s
}`, extra)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "id", "gitlab-configuration"),
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "url", "https://gitlab.com"),
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "provisioning_token_set", "false"),
				),
			},
			{ResourceName: "sonarqube_gitlab_configuration.g", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"secret"}},
			{
				Config: cfg(`url                = "https://gitlab.example.com"
  allowed_groups     = ["acc-group"]
  synchronize_groups = true
  provisioning_token = "glpat-acc"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_gitlab_configuration.g", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "url", "https://gitlab.example.com"),
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "allowed_groups.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_gitlab_configuration.g", "provisioning_token_set", "true"),
				),
			},
		},
	})
}

// The application secret and the provisioning token can be write-only.
func TestAccGitlabConfiguration_writeOnly(t *testing.T) {
	cfg := func(version string) string {
		return fmt.Sprintf(`
resource "sonarqube_gitlab_configuration" "g" {
  enabled                = false
  application_id         = "acc-app"
  secret_wo              = "acc-secret-%[1]s"
  provisioning_token_wo  = "glpat-acc-%[1]s"
  credentials_wo_version = %[1]q
}`, version)
	}
	const name = "sonarqube_gitlab_configuration.g"
	noSecrets := resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckNoResourceAttr(name, "secret"),
		resource.TestCheckNoResourceAttr(name, "secret_wo"),
		resource.TestCheckNoResourceAttr(name, "provisioning_token"),
		resource.TestCheckNoResourceAttr(name, "provisioning_token_wo"),
		resource.TestCheckResourceAttr(name, "provisioning_token_set", "true"),
	)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{
				Config: `
resource "sonarqube_gitlab_configuration" "g" {
  enabled        = false
  application_id = "acc-app"
  secret         = "plain"
  secret_wo      = "wo"
  credentials_wo_version = "1"
}`,
				ExpectError: regexp.MustCompile(`(?s)secret.*secret_wo`),
			},
			{Config: cfg("1"), Check: noSecrets},
			{Config: cfg("2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(name, plancheck.ResourceActionUpdate),
			}}, Check: noSecrets},
		},
	})
}

// testCertificate returns a self-signed PEM certificate.
func testCertificate(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "acc-idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestAccScimProvisioning(t *testing.T) {
	requireEdition(t, "enterprise")
	c := testClient(t)
	enabled := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			s, _, err := c.ScimManagement.Status(context.Background())
			if err != nil {
				return err
			}
			if s.Enabled != want {
				return fmt.Errorf("SCIM enabled = %v, want %v", s.Enabled, want)
			}
			return nil
		}
	}
	// SCIM needs SAML, configured here with settings and a throwaway
	// certificate (one resource each: the test framework rejects for_each).
	samlSettings := [][2]string{
		{"sonar.auth.saml.applicationId", "sonarqube"},
		{"sonar.auth.saml.providerName", "Acc IdP"},
		{"sonar.auth.saml.providerId", "https://idp.example.com"},
		{"sonar.auth.saml.loginUrl", "https://idp.example.com/sso"},
		{"sonar.auth.saml.user.login", "login"},
		{"sonar.auth.saml.user.name", "name"},
		{"sonar.auth.saml.enabled", "true"},
	}
	samlKeys := []string{"sonar.auth.saml.certificate.secured"}
	saml := fmt.Sprintf(`
resource "sonarqube_secured_setting" "saml_cert" {
  key   = "sonar.auth.saml.certificate.secured"
  value = %q
}`, testCertificate(t))
	deps := []string{"sonarqube_secured_setting.saml_cert"}
	for i, kv := range samlSettings {
		saml += fmt.Sprintf(`
resource "sonarqube_setting" "saml%d" {
  key   = %q
  value = %q
}`, i, kv[0], kv[1])
		deps = append(deps, fmt.Sprintf("sonarqube_setting.saml%d", i))
		samlKeys = append(samlKeys, kv[0])
	}
	t.Cleanup(func() { _ = resetSettings(context.Background(), c, "", samlKeys) })
	// SCIM makes users and groups read-only: never leave it enabled. Cleanups
	// run last-in first-out, so this runs before SAML is reset.
	t.Cleanup(func() { _, _ = c.ScimManagement.Disable(context.Background()) })
	scim := saml + fmt.Sprintf(`
resource "sonarqube_scim_provisioning" "s" {
  depends_on = [%s]
}`, strings.Join(deps, ", "))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy:             enabled(false),
		Steps: []resource.TestStep{
			{Config: `resource "sonarqube_scim_provisioning" "s" {}`, ExpectError: regexp.MustCompile(`SAML is not enabled`)},
			{Config: scim, Check: enabled(true)},
			{ResourceName: "sonarqube_scim_provisioning.s", ImportState: true, ImportStateId: "scim", ImportStateVerify: true},
			{
				// Disabled outside Terraform: enabled again.
				PreConfig: func() {
					if _, err := c.ScimManagement.Disable(context.Background()); err != nil {
						t.Fatal(err)
					}
				},
				Config: scim,
				Check:  enabled(true),
			},
		},
	})
}

// SCIM needs Enterprise edition; AUTO_PROVISIONING of GitHub and GitLab
// needs Developer edition (docs.sonarsource.com, SonarQube Server editions).
func TestAccProvisioning_editionRejectedAtPlan(t *testing.T) {
	if ed := testEdition(t); ed != "community" {
		t.Skipf("needs Community Build, server is %s", ed)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: `resource "sonarqube_scim_provisioning" "s" {}`, PlanOnly: true,
				ExpectError: regexp.MustCompile(`SCIM provisioning is not available`),
			},
			{
				Config: `
resource "sonarqube_gitlab_configuration" "g" {
  enabled           = false
  application_id    = "acc-app"
  secret            = "acc-secret"
  allowed_groups    = ["acc-group"]
  provisioning_type = "AUTO_PROVISIONING"
}`, PlanOnly: true,
				ExpectError: regexp.MustCompile(`AUTO_PROVISIONING is not available`),
			},
		},
	})
}
