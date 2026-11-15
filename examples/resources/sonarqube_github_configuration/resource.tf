# Log in with GitHub, and provision the members and teams of one
# organization automatically (a commercial edition is needed for that).
# The *_wo arguments keep credentials out of state (Terraform 1.11+).
resource "sonarqube_github_configuration" "main" {
  enabled                = true
  application_id         = "123456"
  client_id              = "Iv1.0123456789abcdef"
  client_secret_wo       = var.github_client_secret
  private_key_wo         = var.github_private_key
  credentials_wo_version = "1"
  allowed_organizations  = ["my-org"]
  provisioning_type      = "AUTO_PROVISIONING"
}

variable "github_client_secret" {
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
