package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func TestAccPermissions(t *testing.T) {
	p, tpl, u := rname("perm"), rname("tpl"), rname("pusr")
	short := "q" + randSuffix()[:1] // shorter than the API's 3-character search minimum
	cfg := func(projectPerms string, tplDefault bool) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name       = "perm"
  project    = %[1]q
  visibility = "private"
}
resource "sonarqube_user" "u" {
  login_name = %[3]q
  name       = "perm"
  password   = "Acc-Passw0rd-123!"
}
resource "sonarqube_group" "short" {
  name = %[4]q
}
resource "sonarqube_permission_template" "t" {
  name                = %[2]q
  description         = "acc"
  project_key_pattern = "tf-acc-never-.*"
  default             = %[6]t
}
resource "sonarqube_permissions" "global_user" {
  login_name  = sonarqube_user.u.login_name
  permissions = ["gateadmin", "profileadmin"]
}
resource "sonarqube_permissions" "project_group" {
  group_name  = sonarqube_group.short.name
  project_key = sonarqube_project.p.project
  permissions = %[5]s
}
resource "sonarqube_permissions" "template_group" {
  group_name    = sonarqube_group.short.name
  template_name = sonarqube_permission_template.t.name
  permissions   = ["user", "codeviewer"]
}
resource "sonarqube_permissions" "creator" {
  special_group_name = "project_creator"
  template_id        = sonarqube_permission_template.t.id
  permissions        = ["admin"]
}`, p, tpl, u, short, projectPerms, tplDefault)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg(`["user", "codeviewer", "issueadmin"]`, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_permissions.project_group", "permissions.#", "3"),
					resource.TestCheckResourceAttr("sonarqube_permissions.project_group", "id", fmt.Sprintf("group-%s-p_%s-permissions", short, p)),
					resource.TestCheckResourceAttr("sonarqube_permissions.template_group", "permissions.#", "2"),
					resource.TestCheckResourceAttr("sonarqube_permissions.creator", "permissions.#", "1"),
				),
			},
			{ResourceName: "sonarqube_permissions.global_user", ImportState: true, ImportStateId: u, ImportStateVerify: true},
			{ResourceName: "sonarqube_permissions.project_group", ImportState: true, ImportStateId: short + ":p_" + p, ImportStateVerify: true},
			{ResourceName: "sonarqube_permissions.template_group", ImportState: true, ImportStateId: short + ":tn_" + tpl, ImportStateVerify: true},
			{ResourceName: "sonarqube_permission_template.t", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"default"}},
			{ResourceName: "sonarqube_permission_template.t", ImportState: true, ImportStateId: tpl, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"default"}},
			{
				// A permission granted outside Terraform is detected and removed.
				PreConfig: func() {
					_, err := testClient(t).Permissions.AddGroup(context.Background(), &sonarqube.PermissionsAddGroupOptions{GroupName: short, ProjectKey: p, Permission: "admin"})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg(`["user", "codeviewer", "issueadmin"]`, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("sonarqube_permissions.project_group", plancheck.ResourceActionUpdate),
				}},
			},
			{
				// Shrink the set, and make the template default (destroy must hand the default back).
				Config: cfg(`["user"]`, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_permissions.project_group", "permissions.#", "1"),
					resource.TestCheckResourceAttr("sonarqube_permission_template.t", "default", "true"),
				),
			},
		},
	})
}

func TestAccPermissionTemplateDefault(t *testing.T) {
	tpl := rname("tpld")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "sonarqube_permission_template" "t" {
  name = %q
}
resource "sonarqube_permission_template_default" "d" {
  qualifier   = "TRK"
  template_id = sonarqube_permission_template.t.id
}`, tpl),
				Check: resource.TestCheckResourceAttrPair("sonarqube_permission_template_default.d", "template_id", "sonarqube_permission_template.t", "id"),
			},
			{ResourceName: "sonarqube_permission_template_default.d", ImportState: true, ImportStateId: "TRK", ImportStateVerify: true},
		},
	})
}

// On a public project everyone holds user and codeviewer; configuring them
// must converge rather than show a permanent diff.
func TestAccPermissions_publicProject(t *testing.T) {
	p, g := rname("pub"), rname("pubg")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name       = "pub"
  project    = %q
  visibility = "public"
}
resource "sonarqube_group" "g" {
  name = %q
}
resource "sonarqube_permissions" "p" {
  group_name  = sonarqube_group.g.name
  project_key = sonarqube_project.p.project
  permissions = ["user", "codeviewer", "issueadmin"]
}`, p, g),
			Check: resource.TestCheckResourceAttr("sonarqube_permissions.p", "permissions.#", "3"),
		}},
	})
}
