# Pull request decoration requires the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_gitlab_binding" "api" {
  project     = sonarqube_project.api.project
  alm_setting = "gitlab" # key of a sonarqube_alm_gitlab
  repository  = "1234"
  monorepo    = false
}
