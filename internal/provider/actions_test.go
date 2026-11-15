package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// groupHasProjectPermission checks a group's permission on a project.
func groupHasProjectPermission(t *testing.T, project, group, permission string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		res, _, err := testClient(t).Permissions.Groups(context.Background(), &sonarqube.PermissionsGroupsOptions{
			ProjectKey: project, Q: group, Permission: permission})
		if err != nil {
			return err
		}
		has := false
		for _, g := range res.Groups {
			// With a permission filter, only groups that have it are listed.
			if g.Name == group {
				has = true
			}
		}
		if has != want {
			return fmt.Errorf("group %s has %s on %s: %v, want %v", group, permission, project, has, want)
		}
		return nil
	}
}

func TestAccAction_applyPermissionTemplate(t *testing.T) {
	project, tpl, group := rname("actp"), rname("actt"), rname("actg")
	base := fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project = %[1]q
  name    = %[1]q
}
resource "sonarqube_group" "g" {
  name = %[3]q
}
resource "sonarqube_permission_template" "t" {
  name = %[2]q
}
resource "sonarqube_permissions" "t" {
  group_name    = sonarqube_group.g.name
  template_name = sonarqube_permission_template.t.name
  permissions   = ["issueadmin"]
}
`, project, tpl, group)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		Steps: []resource.TestStep{
			{
				// The project predates the template's permission.
				Config: base,
				Check:  groupHasProjectPermission(t, project, group, "issueadmin", false),
			},
			{
				Config: base + `
action "sonarqube_apply_permission_template" "x" {
  config {
    template = "x"
  }
}`,
				ExpectError: regexp.MustCompile(`Set exactly one of projects, search or all_projects`),
			},
			{
				Config: base + `
action "sonarqube_apply_permission_template" "apply" {
  config {
    template = sonarqube_permission_template.t.name
    projects = [sonarqube_project.p.project]
  }
}
resource "terraform_data" "trigger" {
  input = sonarqube_permissions.t.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.sonarqube_apply_permission_template.apply]
    }
  }
}`,
				Check: groupHasProjectPermission(t, project, group, "issueadmin", true),
			},
		},
	})
}

func TestAccAction_sendTestEmailWithoutConfiguration(t *testing.T) {
	if e, err := emailConfig(t); err != nil || e != nil {
		t.Skipf("the server has an email configuration (%v)", err)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		Steps: []resource.TestStep{{
			Config: `
action "sonarqube_send_test_email" "t" {
  config {
    to = "admin@example.com"
  }
}
resource "terraform_data" "trigger" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.sonarqube_send_test_email.t]
    }
  }
}`,
			ExpectError: regexp.MustCompile(`No email configuration`),
		}},
	})
}
