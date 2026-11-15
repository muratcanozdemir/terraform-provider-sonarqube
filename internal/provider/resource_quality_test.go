package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccQualityGate(t *testing.T) {
	g, p, grp := rname("gate"), rname("gatep"), rname("gateg")
	cfg := func(name, cov string, isDefault bool, extra string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "gate"
  project = %[2]q
}
resource "sonarqube_group" "g" {
  name = %[3]q
}
resource "sonarqube_qualitygate" "g" {
  name       = %[1]q
  is_default = %[5]t
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = %[4]q
  }
  %[6]s
}
resource "sonarqube_qualitygate_project_association" "a" {
  gatename   = sonarqube_qualitygate.g.name
  projectkey = sonarqube_project.p.project
}
resource "sonarqube_qualitygate_usergroup_association" "d" {
  gatename   = sonarqube_qualitygate.g.name
  group_name = sonarqube_group.g.name
}`, name, p, grp, cov, isDefault, extra)
	}
	dup := `condition {
    metric    = "new_violations"
    op        = "GT"
    threshold = "0"
  }`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(g, "80", false, dup),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "condition.#", "2"),
					resource.TestCheckResourceAttrSet("sonarqube_qualitygate.g", "condition.0.id"),
					resource.TestCheckResourceAttr("sonarqube_qualitygate_project_association.a", "id", g+"/"+p),
				),
			},
			{ResourceName: "sonarqube_qualitygate.g", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_qualitygate_project_association.a", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"gateid"}},
			{ResourceName: "sonarqube_qualitygate_usergroup_association.d", ImportState: true, ImportStateVerify: true},
			{
				// Rename, threshold change, condition removal and default, all in place.
				Config: cfg(g+"-r", "70", true, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_qualitygate.g", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "id", g+"-r"),
					resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "condition.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "condition.0.threshold", "70"),
					resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "is_default", "true"),
				),
			},
			{
				// Unassigned out of band: the project only falls back to the default gate.
				PreConfig: func() {
					_, err := testClient(t).Qualitygates.Unassign(context.Background(), &sonarqube.QualitygatesUnassignOptions{ProjectKey: p})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config:             cfg(g+"-r", "70", true, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			// Destroying the default gate moves the default back to the built-in gate first.
		},
	})
}

func TestAccQualityGate_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: `resource "sonarqube_qualitygate" "g" { name = "x" }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Missing conditions`)},
			{
				Config: `resource "sonarqube_qualitygate" "g" {
  name = "x"
  condition {
    metric    = "coverage"
    op        = "LT"
    threshold = "1"
  }
  condition {
    metric    = "coverage"
    op        = "LT"
    threshold = "2"
  }
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Duplicate condition`),
			},
		},
	})
}

func TestAccQualityProfile(t *testing.T) {
	parent, child, p := rname("qpp"), rname("qpc"), rname("qpproj")
	cfg := func(childName string, childDefault bool, severity string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "qp"
  project = %[3]q
}
resource "sonarqube_qualityprofile" "parent" {
  name     = %[1]q
  language = "java"
}
resource "sonarqube_qualityprofile" "child" {
  name       = %[2]q
  language   = "java"
  parent     = sonarqube_qualityprofile.parent.name
  is_default = %[4]t
}
resource "sonarqube_qualityprofile_activate_rule" "r" {
  key      = sonarqube_qualityprofile.parent.key
  rule     = "java:S1144"
  severity = %[5]q
}
resource "sonarqube_qualityprofile_deactivate_rule" "d" {
  key  = sonarqube_qualityprofile.child.key
  rule = sonarqube_qualityprofile_activate_rule.r.rule
}
resource "sonarqube_qualityprofile_project_association" "a" {
  quality_profile = sonarqube_qualityprofile.child.name
  project         = sonarqube_project.p.project
  language        = "java"
}
resource "sonarqube_rule" "custom" {
  custom_key           = %[1]q
  template_key         = "java:S124"
  name                 = "No TODO"
  markdown_description = "Avoid TODO"
  params               = "regularExpression=.*TODO.*;message=no"
  severity             = "MINOR"
}`, parent, childName, p, childDefault, severity)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(child, false, "MAJOR"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_qualityprofile.child", "parent", parent),
					resource.TestCheckResourceAttrPair("sonarqube_qualityprofile_activate_rule.r", "key", "sonarqube_qualityprofile.parent", "key"),
					resource.TestCheckResourceAttr("sonarqube_rule.custom", "id", "java:"+parent),
				),
				ExpectNonEmptyPlan: false,
			},
			{ResourceName: "sonarqube_qualityprofile.child", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_qualityprofile.child", ImportState: true, ImportStateId: "java/" + child, ImportStateVerify: true},
			{ResourceName: "sonarqube_qualityprofile_activate_rule.r", ImportState: true, ImportStateVerify: true},
			{ResourceName: "sonarqube_qualityprofile_project_association.a", ImportState: true, ImportStateId: "java/" + child + "/" + p, ImportStateVerify: true},
			{ResourceName: "sonarqube_rule.custom", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"template_key", "markdown_description", "params", "prevent_reactivation"}},
			{
				// Rename, default and severity change in place.
				Config: cfg(child+"-r", true, "BLOCKER"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_qualityprofile.child", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("sonarqube_qualityprofile_activate_rule.r", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("sonarqube_qualityprofile.child", "is_default", "true"),
			},
		},
	})
}

// Destroying a parent profile that still has children is refused.
func TestAccQualityProfile_refusesCascadingDelete(t *testing.T) {
	parent, child := rname("qpp"), rname("qpc")
	c := testClient(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = c.Qualityprofiles.Delete(ctx, &sonarqube.QualityprofilesDeleteOptions{Language: "java", QualityProfile: parent})
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "sonarqube_qualityprofile" "parent" {
  name     = %q
  language = "java"
}`, parent),
			},
			{
				// A child created outside this configuration.
				PreConfig: func() {
					if _, _, err := c.Qualityprofiles.Create(ctx, &sonarqube.QualityprofilesCreateOptions{Name: child, Language: "java"}); err != nil {
						t.Fatal(err)
					}
					if _, err := c.Qualityprofiles.ChangeParent(ctx, &sonarqube.QualityprofilesChangeParentOptions{Language: "java", QualityProfile: child, ParentQualityProfile: parent}); err != nil {
						t.Fatal(err)
					}
				},
				Config:      `# parent removed from configuration`,
				ExpectError: regexp.MustCompile(`has child profiles`),
			},
			{
				PreConfig: func() {
					_, _ = c.Qualityprofiles.Delete(ctx, &sonarqube.QualityprofilesDeleteOptions{Language: "java", QualityProfile: child})
				},
				Config: `# parent removed from configuration`,
			},
		},
	})
}

// Rule activations on a parent and children attached to it run concurrently
// in one apply and one destroy. Every child must end up with every rule, and
// destroy must succeed: unserialized, the server deadlocks deleting children
// while deactivating rules on the parent (HTTP 500).
func TestAccQualityProfile_concurrentInheritance(t *testing.T) {
	const children = 6
	rules := []string{"java:S1144", "java:S1172", "java:S1481", "java:S1068", "java:S125", "java:S1854"}
	name := rname("qpi")
	cfg := fmt.Sprintf(`
locals {
  rules = %[2]s
}
resource "sonarqube_qualityprofile" "parent" {
  name     = %[1]q
  language = "java"
}
resource "sonarqube_qualityprofile_activate_rule" "r" {
  count = length(local.rules)
  key   = sonarqube_qualityprofile.parent.key
  rule  = local.rules[count.index]
}
resource "sonarqube_qualityprofile" "child" {
  count    = %[3]d
  name     = "%[1]s-${count.index}"
  language = "java"
  parent   = sonarqube_qualityprofile.parent.name
}
data "sonarqube_qualityprofile" "child" {
  count      = %[3]d
  key        = sonarqube_qualityprofile.child[count.index].key
  depends_on = [sonarqube_qualityprofile_activate_rule.r]
}
`, name, hclList(rules), children)
	var checks []resource.TestCheckFunc
	for i := range children {
		checks = append(checks, resource.TestCheckResourceAttr(
			fmt.Sprintf("data.sonarqube_qualityprofile.child.%d", i), "active_rule_count", fmt.Sprint(len(rules))))
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check:  resource.ComposeAggregateTestCheckFunc(checks...),
		}},
	})
}

// hclList renders strings as an HCL list literal.
func hclList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
