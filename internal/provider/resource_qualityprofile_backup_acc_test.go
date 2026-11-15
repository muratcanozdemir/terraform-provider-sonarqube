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

func pyBackup(s107max, extra string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><profile><name>ignored</name><language>py</language><rules>` +
		`<rule><repositoryKey>python</repositoryKey><key>BackticksUsage</key><priority>MINOR</priority></rule>` +
		`<rule><repositoryKey>python</repositoryKey><key>S107</key><parameters><parameter><key>max</key><value>` + s107max +
		`</value></parameter></parameters></rule>` + extra + `</rules></profile>`
}

// checkProfileRules checks the rules of the named py profile on the server.
func checkProfileRules(t *testing.T, name string, count int, s107max string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx, c := context.Background(), testClient(t)
		raw, _, err := c.Qualityprofiles.Backup(ctx, &sonarqube.QualityprofilesBackupOptions{Language: "py", QualityProfile: name})
		if err != nil {
			return err
		}
		b, err := parseBackup(string(raw))
		if err != nil {
			return err
		}
		if len(b.Rules) != count {
			return fmt.Errorf("profile %s has %d rules, want %d", name, len(b.Rules), count)
		}
		for _, r := range b.Rules {
			if r.Key == "S107" && (len(r.Parameters) == 0 || r.Parameters[0].Value != s107max) {
				return fmt.Errorf("S107 parameters %+v, want max=%s", r.Parameters, s107max)
			}
		}
		return nil
	}
}

func TestAccQualityProfile_backupAndCopy(t *testing.T) {
	name, renamed, cp := rname("qpbk"), rname("qpbk2"), rname("qpcp")
	cfg := func(name, backup, copyBlock string) string {
		return fmt.Sprintf(`
resource "sonarqube_qualityprofile" "p" {
  name     = %q
  language = "py"
  backup   = %q
}
%s`, name, backup, copyBlock)
	}
	copyBlock := fmt.Sprintf(`
resource "sonarqube_qualityprofile" "c" {
  name      = %q
  language  = "py"
  copy_from = sonarqube_qualityprofile.p.name
}`, cp)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config:      cfg(name, `<profile><language>java</language></profile>`, ""),
				ExpectError: regexp.MustCompile(`Backup of another language`),
			},
			{
				Config:      cfg(name, "<profile>", ""),
				ExpectError: regexp.MustCompile(`not a quality profile backup`),
			},
			{
				Config: fmt.Sprintf(`
resource "sonarqube_qualityprofile" "p" {
  name     = %q
  language = "py"
  parent   = "Sonar way comprehensive"
  backup   = %q
}`, name, pyBackup("9", "")),
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				// SonarQube skips unknown rules on restore; the provider must not.
				Config:      cfg(name, pyBackup("9", `<rule><repositoryKey>python</repositoryKey><key>NoSuchRule</key></rule>`), ""),
				ExpectError: regexp.MustCompile(`(?s)did not apply the whole backup.*1 not active \(python:NoSuchRule\)`),
			},
			{
				Config: cfg(name, pyBackup("9", ""), ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkProfileRules(t, name, 2, "9"),
					resource.TestCheckResourceAttr("sonarqube_qualityprofile.p", "backup", pyBackup("9", "")),
				),
			},
			{
				// A rule activated outside Terraform is drift, and is undone.
				PreConfig: func() {
					ctx, c := context.Background(), testClient(t)
					p, err := findProfile(ctx, c, "", "py", name)
					if err != nil || p == nil {
						t.Fatalf("profile: %v", err)
					}
					if _, err := c.Qualityprofiles.ActivateRule(ctx, &sonarqube.QualityprofilesActivateRuleOptions{Key: p.Key, Rule: "python:S1481"}); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg(name, pyBackup("9", ""), ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_qualityprofile.p", plancheck.ResourceActionUpdate),
				}},
				Check: checkProfileRules(t, name, 2, "9"),
			},
			{
				// Rename and change a parameter in place; copy the result.
				Config: cfg(renamed, pyBackup("7", ""), copyBlock),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_qualityprofile.p", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkProfileRules(t, renamed, 2, "7"),
					checkProfileRules(t, cp, 2, "7"),
					resource.TestCheckResourceAttrPair("sonarqube_qualityprofile.c", "copy_from", "sonarqube_qualityprofile.p", "name"),
					resource.TestCheckNoResourceAttr("sonarqube_qualityprofile.c", "parent"),
				),
			},
			{
				ResourceName:            "sonarqube_qualityprofile.p",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"backup"},
			},
			{
				ResourceName:            "sonarqube_qualityprofile.c",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"copy_from"},
			},
		},
	})
}

func TestAccQualityProfile_copyKeepsParent(t *testing.T) {
	base, child, cp := rname("qpbase"), rname("qpch"), rname("qpcp")
	cfg := func(parent string) string {
		return fmt.Sprintf(`
resource "sonarqube_qualityprofile" "base" {
  name     = %q
  language = "py"
}
resource "sonarqube_qualityprofile" "child" {
  name     = %q
  language = "py"
  parent   = sonarqube_qualityprofile.base.name
}
resource "sonarqube_qualityprofile" "c" {
  name      = %q
  language  = "py"
  copy_from = sonarqube_qualityprofile.child.name
  %s
}`, base, child, cp, parent)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config:      cfg(""),
				ExpectError: regexp.MustCompile(`(?s)a\s+copy\s+keeps\s+that\s+parent:\s+set\s+parent\s+=\s+"tf-acc-qpbase-`),
			},
			{
				Config: cfg(`parent = sonarqube_qualityprofile.base.name`),
				Check:  resource.TestCheckResourceAttrPair("sonarqube_qualityprofile.c", "parent", "sonarqube_qualityprofile.base", "name"),
			},
			{
				// Changing the source re-creates the copy.
				Config: fmt.Sprintf(`
resource "sonarqube_qualityprofile" "base" {
  name     = %q
  language = "py"
}
resource "sonarqube_qualityprofile" "child" {
  name     = %q
  language = "py"
  parent   = sonarqube_qualityprofile.base.name
}
resource "sonarqube_qualityprofile" "c" {
  name      = %q
  language  = "py"
  copy_from = sonarqube_qualityprofile.base.name
}`, base, child, cp),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_qualityprofile.c", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckNoResourceAttr("sonarqube_qualityprofile.c", "parent"),
			},
		},
	})
}
