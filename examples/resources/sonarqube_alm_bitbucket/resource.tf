# The *_wo arguments keep credentials out of state (Terraform 1.11+).
# Change credentials_wo_version to apply new values.
resource "sonarqube_alm_bitbucket" "main" {
  key                      = "bitbucket-server"
  url                      = "https://bitbucket.example.com"
  personal_access_token_wo = var.bitbucket_pat
  credentials_wo_version   = "1"
}

variable "bitbucket_pat" {
  type      = string
  sensitive = true
  ephemeral = true
}
