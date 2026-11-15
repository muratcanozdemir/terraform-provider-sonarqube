# Instance-wide default.
resource "sonarqube_new_code_periods" "global" {
  type  = "NUMBER_OF_DAYS"
  value = "30"
}

# A project that compares against its main branch.
resource "sonarqube_new_code_periods" "api" {
  project = "example-api"
  type    = "REFERENCE_BRANCH"
  value   = "main"
}
