# Pull request decoration requires the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_bitbucket_binding" "api" {
  project     = sonarqube_project.api.project
  alm_setting = "bitbucket-server" # key of a sonarqube_alm_bitbucket
  repository  = "EXAMPLE"
  slug        = "example-api"
  monorepo    = false
}
