resource "sonarqube_permission_template" "projects" {
  name = "projects"
}

resource "sonarqube_permission_template_default" "projects" {
  template_id = sonarqube_permission_template.projects.id
  qualifier   = "TRK"
}
