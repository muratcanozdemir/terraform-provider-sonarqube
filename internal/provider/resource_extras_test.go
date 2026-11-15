package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccSettings(t *testing.T) {
	p := rname("set")
	cfg := func(v string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "set"
  project = %[1]q
}
resource "sonarqube_setting" "global" {
  key   = "sonar.dbcleaner.daysBeforeDeletingClosedIssues"
  value = %[2]q
}
resource "sonarqube_setting" "multi" {
  key       = "sonar.exclusions"
  component = sonarqube_project.p.project
  values    = ["**/a/**", "**/b/**"]
}
resource "sonarqube_setting" "undefined_key" {
  key       = "sonar.links.ci"
  component = sonarqube_project.p.project
  value     = "https://ci.example.com"
}
resource "sonarqube_secured_setting" "s" {
  key   = "sonar.auth.gitlab.secret.secured"
  value = "s3cr3t"
}`, p, v)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("31")},
			{ResourceName: "sonarqube_setting.global", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_setting.multi", ImportState: true, ImportStateId: "sonar.exclusions:" + p, ImportStateVerify: true},
			{ResourceName: "sonarqube_setting.undefined_key", ImportState: true, ImportStateId: "sonar.links.ci:" + p, ImportStateVerify: true},
			{ResourceName: "sonarqube_secured_setting.s", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"value"}},
			{Config: cfg("45"), Check: resource.TestCheckResourceAttr("sonarqube_setting.global", "value", "45")},
			{
				// Reset outside Terraform: back to the default, so re-set.
				PreConfig: func() {
					if err := resetSettings(context.Background(), testClient(t), "", []string{"sonar.dbcleaner.daysBeforeDeletingClosedIssues"}); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("45"), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccSettings_invalid checks that values the API rejects, or cannot read back,
// fail at plan rather than half-way through an apply.
func TestAccSettings_invalid(t *testing.T) {
	steps := []resource.TestStep{}
	for _, c := range []struct{ attr, cfg string }{
		{"value", `resource "sonarqube_setting" "s" {
  key   = "sonar.exclusions"
  value = ""
}`},
		{"values", `resource "sonarqube_setting" "s" {
  key    = "sonar.exclusions"
  values = []
}`},
		{"field_values", `resource "sonarqube_setting" "s" {
  key          = "sonar.issue.ignore.multicriteria"
  field_values = []
}`},
		{"setting block value", `resource "sonarqube_project" "p" {
  name    = "empty"
  project = "tf-acc-empty-setting"
  setting {
    key   = "sonar.exclusions"
    value = ""
  }
}`},
	} {
		steps = append(steps, resource.TestStep{Config: c.cfg, PlanOnly: true,
			ExpectError: regexp.MustCompile(`(?s)(length must be at least 1|must contain at least 1 elements)`)})
	}
	// A secured key would look deleted on every refresh.
	steps = append(steps, resource.TestStep{PlanOnly: true, ExpectError: regexp.MustCompile(`sonarqube_secured_setting`),
		Config: `resource "sonarqube_setting" "s" {
  key   = "sonar.auth.gitlab.secret.secured"
  value = "x"
}`})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps:                    steps,
	})
}

func TestAccSecuredSetting_writeOnly(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{Config: `
resource "sonarqube_secured_setting" "s" {
  key              = "sonar.auth.gitlab.applicationId.secured"
  value_wo         = "app-1"
  value_wo_version = "1"
}`, Check: resource.TestCheckNoResourceAttr("sonarqube_secured_setting.s", "value_wo")},
			{Config: `
resource "sonarqube_secured_setting" "s" {
  key              = "sonar.auth.gitlab.applicationId.secured"
  value_wo         = "app-2"
  value_wo_version = "2"
}`},
		},
	})
}

func TestAccWebhook(t *testing.T) {
	p := rname("wh")
	cfg := func(secret string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "wh"
  project = %q
}
resource "sonarqube_webhook" "global" {
  name = "global"
  url  = "https://hooks.example.com/global"
  %s
}
resource "sonarqube_webhook" "project" {
  name    = "proj"
  url     = "https://hooks.example.com/p"
  project = sonarqube_project.p.project
}`, p, secret)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg(`secret = "0123456789abcdef"`)},
			{ResourceName: "sonarqube_webhook.global", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secret"}},
			{ResourceName: "sonarqube_webhook.project", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["sonarqube_webhook.project"].Primary.ID + "/" + p, nil
				}},
			// Removing the secret from configuration removes it from the webhook.
			{Config: cfg(""), Check: resource.TestCheckNoResourceAttr("sonarqube_webhook.global", "secret")},
		},
	})
}

func TestAccNewCodePeriods(t *testing.T) {
	p := rname("ncp")
	cfg := func(days string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "ncp"
  project = %q
}
resource "sonarqube_new_code_periods" "project" {
  project = sonarqube_project.p.project
  type    = "NUMBER_OF_DAYS"
  value   = %q
}`, p, days)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			// On Community Build this is stored on the main branch; it must still converge.
			{Config: cfg("30")},
			{ResourceName: "sonarqube_new_code_periods.project", ImportState: true, ImportStateId: p, ImportStateVerify: true},
			{Config: cfg("15"), Check: resource.TestCheckResourceAttr("sonarqube_new_code_periods.project", "value", "15")},
		},
	})
}

func TestAccProjectLinksAndMainBranch(t *testing.T) {
	p := rname("lnk")
	cfg := func(branch string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "lnk"
  project = %q
}
resource "sonarqube_project_links" "l" {
  project_key = sonarqube_project.p.project
  name        = "Runbook"
  url         = "https://runbooks.example.com/x"
}
resource "sonarqube_project_main_branch" "b" {
  project = sonarqube_project.p.project
  name    = %q
}`, p, branch)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("develop")},
			{ResourceName: "sonarqube_project_links.l", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return p + "/" + s.RootModule().Resources["sonarqube_project_links.l"].Primary.ID, nil
				}},
			{ResourceName: "sonarqube_project_main_branch.b", ImportState: true, ImportStateId: p + "/develop", ImportStateVerify: true},
			{Config: cfg("trunk"), Check: resource.TestCheckResourceAttr("sonarqube_project_main_branch.b", "name", "trunk")},
		},
	})
}

// A write-only webhook secret is not mistaken for one set outside
// Terraform, rotates with its version, and is removed with it.
func TestAccWebhook_writeOnly(t *testing.T) {
	name := rname("whwo")
	cfg := func(secret string) string {
		return fmt.Sprintf(`
resource "sonarqube_webhook" "w" {
  name = %q
  url  = "https://hooks.example.com/wo"
  %s
}`, name, secret)
	}
	hasSecret := func(want bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			key := s.RootModule().Resources["sonarqube_webhook.w"].Primary.ID
			res, _, err := testClient(t).Webhooks.List(context.Background(), &sonarqube.WebhooksListOptions{})
			if err != nil {
				return err
			}
			for _, w := range res.Webhooks {
				if w.Key == key {
					if w.HasSecret != want {
						return fmt.Errorf("webhook has secret: %v, want %v", w.HasSecret, want)
					}
					return nil
				}
			}
			return fmt.Errorf("webhook %s not found", key)
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{Config: cfg(`secret_wo = "0123456789abcdef-one"
  secret_wo_version = "1"`), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("sonarqube_webhook.w", "secret"),
				resource.TestCheckNoResourceAttr("sonarqube_webhook.w", "secret_wo"),
				hasSecret(true),
			)},
			{Config: cfg(`secret_wo = "0123456789abcdef-two"
  secret_wo_version = "2"`), ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("sonarqube_webhook.w", plancheck.ResourceActionUpdate)},
			}, Check: hasSecret(true)},
			{Config: cfg(""), Check: hasSecret(false)},
		},
	})
}

// TestAccPlugin installs a Marketplace plugin, which stays pending until a
// restart, and destroys it, which must cancel only that installation.
func TestAccPlugin(t *testing.T) {
	if ed := testEdition(t); ed != "community" {
		t.Skipf("the Marketplace is only in Community Build, server is %s", ed)
	}
	const key, other = "hadolint", "scmcvs"
	c := testClient(t)
	ctx := context.Background()
	avail, _, err := c.Plugins.Available(ctx)
	if err != nil {
		t.Skipf("Marketplace not reachable: %v", err)
	}
	found := map[string]bool{}
	for _, p := range avail.Plugins {
		found[p.Key] = true
	}
	if !found[key] || !found[other] {
		t.Skipf("plugins %s and %s are not both available from the Marketplace", key, other)
	}
	t.Cleanup(func() {
		_, _ = c.Plugins.CancelAll(ctx)
		_ = resetSettings(ctx, c, "", []string{"sonar.plugins.risk.consent"})
	})
	pending := func(k string) (bool, error) {
		r := &pluginResource{resourceBase{&providerData{client: c}}}
		p, err := r.pending(ctx)
		return err == nil && p.installing(k), err
	}
	plugin := fmt.Sprintf(`
resource "sonarqube_plugin" "p" {
  key = %q
}`, key)
	consent := `
resource "sonarqube_setting" "consent" {
  key   = "sonar.plugins.risk.consent"
  value = "ACCEPTED"
}`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy: func(*terraform.State) error {
			if ok, err := pending(key); err != nil || ok {
				return fmt.Errorf("installation of %s still pending (err %v)", key, err)
			}
			if ok, err := pending(other); err != nil || !ok {
				return fmt.Errorf("the pending installation of %s was not restored (err %v)", other, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// The server's own refusal, then the provider's hint.
				Config:      plugin,
				ExpectError: regexp.MustCompile(`(?s)without accepting firstly plugins risk consent.*sonar.plugins.risk.consent`),
			},
			{
				Config: consent + strings.Replace(plugin, "}", "  depends_on = [sonarqube_setting.consent]\n}", 1),
				Check:  resource.TestCheckResourceAttr("sonarqube_plugin.p", "restart_required", "true"),
			},
			{ResourceName: "sonarqube_plugin.p", ImportState: true, ImportStateId: key, ImportStateVerify: true},
			{
				// Another plugin pending installation, outside Terraform: the
				// destroy that follows must leave it pending.
				PreConfig: func() {
					if _, err := c.Plugins.Install(ctx, &sonarqube.PluginsInstallOptions{Key: other}); err != nil {
						t.Fatal(err)
					}
				},
				Config: consent + strings.Replace(plugin, "}", "  depends_on = [sonarqube_setting.consent]\n}", 1),
			},
		},
	})
}

func TestAccPlugin_commercialRejectedAtPlan(t *testing.T) {
	requireEdition(t, "developer")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config:      `resource "sonarqube_plugin" "p" { key = "checkstyle" }`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`The Marketplace is not available`),
		}},
	})
}

// TestAccPlugin_commercialDestroyForgets imports a plugin bundled with a
// commercial edition, e.g. one in state migrated from jdamata, and destroys
// it: the plugin must stay installed, since such editions remove plugins by
// hand.
func TestAccPlugin_commercialDestroyForgets(t *testing.T) {
	requireEdition(t, "developer")
	const key = "abap"
	c := testClient(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = c.Plugins.CancelAll(ctx) })
	cfg := fmt.Sprintf(`
resource "sonarqube_plugin" "p" {
  key = %q
}`, key)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy: func(*terraform.State) error {
			r := &pluginResource{resourceBase{&providerData{client: c}}}
			present, pending, err := r.status(ctx, key)
			if err != nil {
				return err
			}
			if !present || pending {
				return fmt.Errorf("plugin %s: installed %v, pending change %v; want installed and unchanged", key, present, pending)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, ResourceName: "sonarqube_plugin.p", ImportState: true, ImportStateId: key, ImportStatePersist: true},
			{
				Config:           cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            resource.TestCheckResourceAttr("sonarqube_plugin.p", "restart_required", "false"),
			},
		},
	})
}
