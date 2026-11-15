resource "sonarqube_permission_template" "internal" {
  name                = "internal"
  description         = "Projects whose keys start with internal-."
  project_key_pattern = "internal-.*"
}
