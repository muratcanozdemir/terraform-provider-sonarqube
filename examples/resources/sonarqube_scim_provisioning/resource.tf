# SCIM needs SAML authentication, configured first with its required
# settings. Once SCIM is enabled, users and groups are managed in the
# identity provider and are read-only in SonarQube.
locals {
  saml = {
    "sonar.auth.saml.applicationId" = "sonarqube"
    "sonar.auth.saml.providerName"  = "Entra ID"
    "sonar.auth.saml.providerId"    = "https://sts.windows.net/00000000-0000-0000-0000-000000000000/"
    "sonar.auth.saml.loginUrl"      = "https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/saml2"
    "sonar.auth.saml.user.login"    = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"
    "sonar.auth.saml.user.name"     = "http://schemas.microsoft.com/identity/claims/displayname"
    "sonar.auth.saml.enabled"       = "true"
  }
}

resource "sonarqube_setting" "saml" {
  for_each = local.saml
  key      = each.key
  value    = each.value
}

resource "sonarqube_secured_setting" "saml_certificate" {
  key              = "sonar.auth.saml.certificate.secured"
  value_wo         = var.idp_certificate
  value_wo_version = "1"
}

variable "idp_certificate" {
  description = "PEM certificate of the identity provider."
  type        = string
  sensitive   = true
  ephemeral   = true
}

resource "sonarqube_scim_provisioning" "main" {
  depends_on = [sonarqube_setting.saml, sonarqube_secured_setting.saml_certificate]
}
