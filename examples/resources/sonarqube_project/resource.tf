resource "sonarqube_project" "api" {
  name       = "API"
  project    = "example-api"
  visibility = "private"
  tags       = ["backend", "payments"]

  # When any setting block is present, the list is authoritative.
  setting {
    key    = "sonar.exclusions"
    values = ["**/generated/**", "**/vendor/**"]
  }
}
