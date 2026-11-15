# A global webhook. secret_wo keeps the signing secret out of state
# (Terraform 1.11+); change secret_wo_version to apply a new one.
resource "sonarqube_webhook" "ci" {
  name              = "ci"
  url               = "https://ci.example.com/sonarqube/webhook"
  secret_wo         = var.webhook_secret
  secret_wo_version = "1"
}

# A project webhook.
resource "sonarqube_webhook" "api" {
  name    = "api-deploy"
  url     = "https://deploy.example.com/hooks/sonarqube"
  project = "example-api"
}

variable "webhook_secret" {
  type      = string
  sensitive = true
  ephemeral = true
}
