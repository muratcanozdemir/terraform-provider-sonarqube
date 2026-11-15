resource "sonarqube_project_links" "docs" {
  project_key = "example-api"
  name        = "Documentation"
  url         = "https://docs.example.com/api"
}
