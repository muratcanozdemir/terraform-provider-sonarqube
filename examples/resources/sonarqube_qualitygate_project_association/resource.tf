resource "sonarqube_qualitygate" "strict" {
  name = "Strict"

  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
}

resource "sonarqube_qualitygate_project_association" "api" {
  gatename   = sonarqube_qualitygate.strict.name
  projectkey = "example-api"
}
