# The *_wo arguments keep credentials out of state (Terraform 1.11+).
# Change credentials_wo_version to apply new values.
resource "sonarqube_alm_github" "main" {
  key                    = "github"
  url                    = "https://api.github.com/"
  app_id                 = "123456"
  client_id              = "Iv1.0123456789abcdef"
  client_secret_wo       = var.github_client_secret
  private_key_wo         = var.github_private_key
  webhook_secret_wo      = var.github_webhook_secret
  credentials_wo_version = "1"
}

variable "github_client_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "github_webhook_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "github_private_key" {
  description = "PEM private key of the GitHub App."
  type        = string
  sensitive   = true
  ephemeral   = true
}
