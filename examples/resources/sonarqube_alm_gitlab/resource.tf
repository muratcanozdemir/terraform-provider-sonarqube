# The *_wo arguments keep credentials out of state (Terraform 1.11+).
# Change credentials_wo_version to apply new values.
resource "sonarqube_alm_gitlab" "main" {
  key                      = "gitlab"
  url                      = "https://gitlab.example.com/api/v4"
  personal_access_token_wo = var.gitlab_pat
  credentials_wo_version   = "1"
}

variable "gitlab_pat" {
  type      = string
  sensitive = true
  ephemeral = true
}
