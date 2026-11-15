# value_wo keeps the secret out of state (Terraform 1.11+). Bump
# value_wo_version to apply a new value.
resource "sonarqube_secured_setting" "github_auth_secret" {
  key              = "sonar.auth.github.clientSecret.secured"
  value_wo         = var.github_oauth_client_secret
  value_wo_version = "1"
}

variable "github_oauth_client_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}
