# Links an existing user to their identity in an external provider. The
# provider must be configured on the server, e.g. LDAP as `LDAP_default`.
resource "sonarqube_user_external_identity" "alice" {
  login_name        = "alice"
  external_provider = "LDAP_default"
  external_identity = "alice"
}
