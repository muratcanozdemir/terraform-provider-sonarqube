# A global permission for a group.
resource "sonarqube_permissions" "ci_scan" {
  group_name  = "ci"
  permissions = ["scan"]
}

# Project permissions for a user.
resource "sonarqube_permissions" "alice_api" {
  login_name  = "alice"
  project_key = "example-api"
  permissions = ["admin", "issueadmin"]
}

# The creator of each project gets admin, through a template.
resource "sonarqube_permission_template" "projects" {
  name = "projects"
}

resource "sonarqube_permissions" "creator" {
  special_group_name = "project_creator"
  template_name      = sonarqube_permission_template.projects.name
  permissions        = ["admin"]
}
