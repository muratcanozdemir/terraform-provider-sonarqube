# Application branches require the Developer edition or higher. The project
# branches must exist (they are created by analysis).
resource "sonarqube_application_branch" "release" {
  application = sonarqube_application.shop.key
  name        = "release-2.4"
  projects = {
    (sonarqube_project.api.project) = "release/2.4"
    (sonarqube_project.web.project) = "" # main branch
  }
}
