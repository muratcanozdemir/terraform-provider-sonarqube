# The *_wo arguments keep credentials out of state (Terraform 1.11+).
# Change credentials_wo_version to apply new values.
# Bitbucket Cloud requires the Enterprise edition or higher when a Bitbucket
# Server setting also exists.
resource "sonarqube_alm_bitbucket_cloud" "main" {
  key                    = "bitbucket-cloud"
  workspace              = "example-workspace"
  client_id              = var.bitbucket_client_id
  client_secret_wo       = var.bitbucket_client_secret
  credentials_wo_version = "1"
}

variable "bitbucket_client_id" {
  type = string
}

variable "bitbucket_client_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}
