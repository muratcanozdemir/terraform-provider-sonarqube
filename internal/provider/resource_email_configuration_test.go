package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func emailConfig(t *testing.T) (*sonarqube.EmailConfigurationResource, error) {
	res, _, err := testClient(t).SystemV2.SearchEmailConfigurations(context.Background())
	if err != nil || len(res.EmailConfigurations) == 0 {
		return nil, err
	}
	return res.EmailConfigurations[0], nil
}

func checkEmailConfig(t *testing.T, check func(*sonarqube.EmailConfigurationResource) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		e, err := emailConfig(t)
		if err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("no email configuration on the server")
		}
		return check(e)
	}
}

func TestAccEmailConfiguration(t *testing.T) {
	if e, err := emailConfig(t); err != nil || e != nil {
		t.Skipf("the server already has an email configuration (%v)", err)
	}
	basic := func(host string, port int, creds string) string {
		return fmt.Sprintf(`
resource "sonarqube_email_configuration" "e" {
  host              = %q
  port              = %d
  security_protocol = "STARTTLS"
  from_address      = "noreply@example.com"
  username          = "sonar"
  %s
}`, host, port, creds)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		CheckDestroy: func(*terraform.State) error {
			if e, err := emailConfig(t); err != nil || e != nil {
				return fmt.Errorf("email configuration left behind: %+v, %w", e, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      basic("smtp.example.com", 587, ""),
				ExpectError: regexp.MustCompile(`needs basic_password or basic_password_wo`),
			},
			{
				Config: basic("smtp.example.com", 587, `basic_password_wo = "s3cret"
  credentials_wo_version = "1"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_email_configuration.e", "id", "email-configuration"),
					resource.TestCheckResourceAttr("sonarqube_email_configuration.e", "auth_method", "BASIC"),
					resource.TestCheckResourceAttr("sonarqube_email_configuration.e", "from_name", "SonarQube"),
					resource.TestCheckResourceAttr("sonarqube_email_configuration.e", "subject_prefix", "[SONARQUBE]"),
					resource.TestCheckNoResourceAttr("sonarqube_email_configuration.e", "basic_password_wo"),
					checkEmailConfig(t, func(e *sonarqube.EmailConfigurationResource) error {
						if e.Host != "smtp.example.com" || e.Port != "587" || !e.IsBasicPasswordSet {
							return fmt.Errorf("server has %+v", e)
						}
						return nil
					}),
				),
			},
			{
				// Changing other settings keeps the password.
				Config: basic("mail.example.com", 465, `basic_password_wo = "s3cret"
  credentials_wo_version = "1"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_email_configuration.e", plancheck.ResourceActionUpdate),
				}},
				Check: checkEmailConfig(t, func(e *sonarqube.EmailConfigurationResource) error {
					if e.Host != "mail.example.com" || e.Port != "465" || !e.IsBasicPasswordSet {
						return fmt.Errorf("server has %+v", e)
					}
					return nil
				}),
			},
			{
				ResourceName:            "sonarqube_email_configuration.e",
				ImportState:             true,
				ImportStateId:           "email-configuration",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"credentials_wo_version"},
			},
			{
				Config: `
resource "sonarqube_email_configuration" "e" {
  host                      = "smtp.office365.com"
  port                      = 587
  security_protocol         = "STARTTLS"
  from_address              = "noreply@example.com"
  from_name                 = "Sonar"
  subject_prefix            = "[SQ]"
  username                  = "sonar@example.com"
  auth_method               = "OAUTH"
  oauth_authentication_host = "login.microsoftonline.com"
  oauth_tenant              = "example.onmicrosoft.com"
  oauth_client_id           = "client"
  oauth_client_secret_wo    = "secret"
  credentials_wo_version    = "2"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_email_configuration.e", "oauth_client_id", "client"),
					checkEmailConfig(t, func(e *sonarqube.EmailConfigurationResource) error {
						if e.AuthMethod != "OAUTH" || e.OauthTenant != "example.onmicrosoft.com" || e.FromName != "Sonar" ||
							!e.IsOauthClientIDSet || !e.IsOauthClientSecretSet {
							return fmt.Errorf("server has %+v", e)
						}
						return nil
					}),
				),
			},
			{
				// Deleted outside Terraform: created again.
				PreConfig: func() {
					if _, err := testClient(t).SystemV2.DeleteEmailConfiguration(context.Background(),
						&sonarqube.SystemV2DeleteEmailConfigurationOptions{ID: "email-configuration"}); err != nil {
						t.Fatal(err)
					}
				},
				Config: basic("smtp.example.com", 25, `basic_password = "s3cret"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_email_configuration.e", plancheck.ResourceActionCreate),
				}},
				Check: checkEmailConfig(t, func(e *sonarqube.EmailConfigurationResource) error {
					if e.Port != "25" || !e.IsBasicPasswordSet {
						return fmt.Errorf("server has %+v", e)
					}
					return nil
				}),
			},
		},
	})
}
