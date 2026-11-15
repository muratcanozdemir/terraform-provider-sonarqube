# The *_wo arguments keep credentials out of state (Terraform 1.11+).
# Change credentials_wo_version to apply new values.
resource "sonarqube_alm_azure" "main" {
  key                      = "azure-devops"
  url                      = "https://dev.azure.com/example-org"
  personal_access_token_wo = var.azure_pat
  credentials_wo_version   = "1"
}

variable "azure_pat" {
  type      = string
  sensitive = true
  ephemeral = true
}
