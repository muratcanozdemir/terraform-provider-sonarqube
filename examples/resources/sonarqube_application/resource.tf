# Applications require the Developer edition or higher.
resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_project" "web" {
  name    = "Web"
  project = "example-web"
}

resource "sonarqube_application" "shop" {
  key         = "example-shop"
  name        = "Shop"
  description = "Everything the shop is built from."
  visibility  = "private"
  projects    = [sonarqube_project.api.project, sonarqube_project.web.project]
}
