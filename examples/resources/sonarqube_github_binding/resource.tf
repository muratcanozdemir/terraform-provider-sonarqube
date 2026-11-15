# Pull request decoration requires the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_github_binding" "api" {
  project     = sonarqube_project.api.project
  alm_setting = "github" # key of a sonarqube_alm_github
  repository  = "example-org/example-api"
  monorepo    = false
}
