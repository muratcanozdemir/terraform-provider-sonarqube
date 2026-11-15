package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccGroupAndMembers(t *testing.T) {
	g, u := rname("grp"), rname("usr")
	cfg := func(group, desc string) string {
		return fmt.Sprintf(`
resource "sonarqube_user" "u" {
  login_name = %[2]q
  name       = "Acc User"
  email      = "acc@example.com"
  password   = "Acc-Passw0rd-123!"
}
resource "sonarqube_group" "g" {
  name        = %[1]q
  description = %[3]q
}
resource "sonarqube_group_member" "m" {
  name       = sonarqube_group.g.name
  login_name = sonarqube_user.u.login_name
}`, group, u, desc)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(g, "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("sonarqube_group.g", "id"),
					resource.TestCheckResourceAttr("sonarqube_group_member.m", "id", fmt.Sprintf("%s[%s]", g, u)),
					resource.TestCheckResourceAttrSet("sonarqube_user.u", "uuid"),
				),
			},
			{ResourceName: "sonarqube_group.g", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_group.g", ImportState: true, ImportStateId: g, ImportStateVerify: true},
			{ResourceName: "sonarqube_group_member.m", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_user.u", ImportState: true, ImportStateId: u, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"password"}},
			{
				// Group rename and description clear in place; the membership follows by replacement.
				Config: cfg(g+"-new", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_group.g", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("sonarqube_group.g", "description", ""),
			},
		},
	})
}

func TestAccUser_inPlaceChanges(t *testing.T) {
	u := rname("usr")
	cfg := func(login, name, pw string, scm ...string) string {
		if len(scm) == 0 {
			scm = []string{"scm-" + u}
		}
		accounts, _ := json.Marshal(scm)
		return fmt.Sprintf(`
resource "sonarqube_user" "u" {
  login_name   = %q
  name         = %q
  password     = %q
  scm_accounts = %s
}`, login, name, pw, accounts)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg(u, "One", "Acc-Passw0rd-123!")},
			{
				// Login, name and password change in place; upstream replaced the user.
				Config: cfg(u+"-x", "Two", "Acc-Passw0rd-456!"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_user.u", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_user.u", "id", u+"-x"),
					resource.TestCheckResourceAttr("sonarqube_user.u", "name", "Two"),
					resource.TestCheckResourceAttr("sonarqube_user.u", "scm_accounts.#", "1"),
				),
			},
			{
				// SCM accounts change in place.
				Config: cfg(u+"-x", "Two", "Acc-Passw0rd-456!", "scm-a-"+u, "scm-b-"+u),
				Check:  resource.TestCheckResourceAttr("sonarqube_user.u", "scm_accounts.#", "2"),
			},
		},
	})
}

func TestAccUser_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: `resource "sonarqube_user" "bad" {
  login_name = "nopw"
  name       = "x"
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Missing password`),
		}},
	})
}

func TestAccUser_writeOnlyPassword(t *testing.T) {
	u := rname("wo")
	cfg := func(v string) string {
		return fmt.Sprintf(`
resource "sonarqube_user" "u" {
  login_name          = %q
  name                = "WO"
  password_wo         = "Acc-Passw0rd-%s!"
  password_wo_version = %q
}`, u, v, v)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{Config: cfg("1"), Check: resource.TestCheckNoResourceAttr("sonarqube_user.u", "password_wo")},
			{Config: cfg("2")},
		},
	})
}

func TestAccUserExternalIdentity(t *testing.T) {
	u := rname("ext")
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			// An identity provider must be enabled; GitLab needs only two dummy credentials.
			c := testClient(t)
			keys := []string{"sonar.auth.gitlab.applicationId.secured", "sonar.auth.gitlab.secret.secured", "sonar.auth.gitlab.enabled"}
			for i, v := range []string{"dummy", "dummy", "true"} {
				if err := setSetting(context.Background(), c, "", keys[i], v, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			// Leave no GitLab configuration behind for the tests that manage one.
			t.Cleanup(func() {
				if err := resetSettings(context.Background(), c, "", keys); err != nil {
					t.Errorf("resetting GitLab settings: %v", err)
				}
			})
		},
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_user" "u" {
  login_name = %[1]q
  name       = "Ext"
  is_local   = false
}
resource "sonarqube_user_external_identity" "e" {
  login_name        = sonarqube_user.u.login_name
  external_identity = "%[1]s-id"
  external_provider = "gitlab"
}`, u),
			Check: resource.TestCheckResourceAttr("sonarqube_user_external_identity.e", "external_identity", u+"-id"),
		}},
	})
}

func TestAccUserToken(t *testing.T) {
	u, p := rname("tok"), rname("tokp")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "sonarqube_user" "u" {
  login_name = %q
  name       = "Tok"
  password   = "Acc-Passw0rd-123!"
}
resource "sonarqube_project" "p" {
  name    = "tok"
  project = %q
}
resource "sonarqube_user_token" "user" {
  name       = "t1"
  login_name = sonarqube_user.u.login_name
}
resource "sonarqube_user_token" "proj" {
  name            = "t2"
  type            = "PROJECT_ANALYSIS_TOKEN"
  project_key     = sonarqube_project.p.project
  expiration_date = "2099-01-01"
}`, u, p),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("sonarqube_user_token.user", "token"),
					resource.TestCheckResourceAttr("sonarqube_user_token.user", "id", u+"/t1"),
					resource.TestCheckResourceAttr("sonarqube_user_token.proj", "expiration_date", "2099-01-01"),
				),
			},
			{ResourceName: "sonarqube_user_token.user", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"token", "metadata_only"}},
			{
				// A token revoked outside Terraform is re-issued.
				PreConfig: func() {
					if _, err := testClient(t).UserTokens.Revoke(context.Background(), &sonarqube.UserTokensRevokeOptions{Login: u, Name: "t1"}); err != nil {
						t.Fatal(err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccUserTokenEphemeral(t *testing.T) {
	prefix := rname("eph") + "-"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			// The ephemeral token authenticates a second provider instance
			// during the run, then is revoked.
			Config: fmt.Sprintf(`
ephemeral "sonarqube_user_token" "t" {
  name_prefix = %q
}
provider "sonarqube" {
  alias = "eph"
  token = ephemeral.sonarqube_user_token.t.token
}
data "sonarqube_server" "s" {
  provider = sonarqube.eph
}`, prefix),
			Check: func(*terraform.State) error {
				_, tokens, err := listTokens(context.Background(), testClient(t), "")
				if err != nil {
					return err
				}
				for _, tok := range tokens {
					if strings.HasPrefix(tok.Name, prefix) {
						return fmt.Errorf("ephemeral token %s was not revoked", tok.Name)
					}
				}
				return nil
			},
		}},
	})
}

// TestAccUserToken_rotation moves the provider's clock into the rotation
// window and expects the token to be replaced by one with a new lifetime.
func TestAccUserToken_rotation(t *testing.T) {
	realToday := today
	t.Cleanup(func() { today = realToday })
	shift := func(days int) {
		today = func() time.Time { return realToday().AddDate(0, 0, days) }
	}
	date := func(days int) string { return realToday().AddDate(0, 0, days).Format("2006-01-02") }

	name := rname("rot")
	cfg := fmt.Sprintf(`
resource "sonarqube_user_token" "t" {
  name               = %q
  expiration_days    = 30
  rotate_before_days = 7
}`, name)
	var first string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_user_token.t", "expiration_date", date(30)),
					resource.TestCheckResourceAttrWith("sonarqube_user_token.t", "token", func(v string) error { first = v; return nil }),
				),
			},
			{
				// 8 days left: outside the 7-day window, nothing to do.
				PreConfig: func() { shift(22) },
				Config:    cfg, PlanOnly: true,
			},
			{
				// 6 days left: replaced, with 30 days from the (shifted) day of issue.
				PreConfig: func() { shift(24) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_user_token.t", plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_user_token.t", "expiration_date", date(54)),
					resource.TestCheckResourceAttrWith("sonarqube_user_token.t", "token", func(v string) error {
						if v == first {
							return fmt.Errorf("token was not reissued")
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccUserToken_rotationValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{PlanOnly: true, ExpectError: regexp.MustCompile(`must be less than expiration_days`), Config: `
resource "sonarqube_user_token" "t" {
  name               = "x"
  expiration_days    = 7
  rotate_before_days = 7
}`},
			{PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)expiration_days.*expiration_date|expiration_date.*expiration_days`), Config: `
resource "sonarqube_user_token" "t" {
  name            = "x"
  expiration_days = 7
  expiration_date = "2099-01-01"
}`},
			{PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)rotate_before_days.*expiration_days`), Config: `
resource "sonarqube_user_token" "t" {
  name               = "x"
  rotate_before_days = 7
}`},
			{PlanOnly: true, ExpectError: regexp.MustCompile(`Cannot rotate an adopted token`), Config: `
resource "sonarqube_user_token" "t" {
  name            = "x"
  metadata_only   = true
  expiration_days = 7
}`},
		},
	})
}

// SonarQube issues analysis tokens only to the caller; asking for one on
// behalf of another user fails at plan time rather than at apply.
func TestAccUserToken_analysisTokenForAnotherUserRejected(t *testing.T) {
	login := rname("other")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_user_token" "t" {
  name       = "ci"
  login_name = %q
  type       = "GLOBAL_ANALYSIS_TOKEN"
}`, login),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Analysis token for another user`),
		}},
	})
}
