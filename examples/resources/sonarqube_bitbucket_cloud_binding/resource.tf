# Pull request decoration requires the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_bitbucket_cloud_binding" "api" {
  project     = sonarqube_project.api.project
  alm_setting = "bitbucket-cloud" # key of a sonarqube_alm_bitbucket_cloud
  repository  = "example-api"
  monorepo    = false
}
