# Log in with a self-managed GitLab, adding users at their first login to the
# SonarQube groups named like their GitLab groups.
resource "sonarqube_gitlab_configuration" "main" {
  enabled                = true
  url                    = "https://gitlab.example.com"
  application_id         = "0123456789abcdef"
  secret_wo              = var.gitlab_secret
  credentials_wo_version = "1"
  allowed_groups         = ["engineering"]
  provisioning_type      = "JIT"
  synchronize_groups     = true
}

variable "gitlab_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}
