# A token for another user (requires administrator rights).
resource "sonarqube_user_token" "ci" {
  login_name      = "ci-bot"
  name            = "ci"
  type            = "USER_TOKEN"
  expiration_date = "2027-12-31"
}

# A global analysis token for the user Terraform authenticates as.
resource "sonarqube_user_token" "scanner" {
  name = "scanner"
  type = "GLOBAL_ANALYSIS_TOKEN"
}

# Token values are only available on creation.
output "ci_token" {
  value     = sonarqube_user_token.ci.token
  sensitive = true
}
