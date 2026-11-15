# Pull request decoration requires the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_azure_binding" "api" {
  project         = sonarqube_project.api.project
  alm_setting     = "azure-devops" # key of a sonarqube_alm_azure
  project_name    = "Example"
  repository_name = "example-api"
  monorepo        = false
}
