# A local user. password_wo keeps the password out of state (Terraform 1.11+).
resource "sonarqube_user" "ci" {
  login_name          = "ci-bot"
  name                = "CI bot"
  email               = "ci-bot@example.com"
  password_wo         = var.ci_password
  password_wo_version = "1"
}

variable "ci_password" {
  type      = string
  sensitive = true
  ephemeral = true
}
