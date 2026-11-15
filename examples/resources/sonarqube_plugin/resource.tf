# Installing from the Marketplace needs the administrator's consent to the
# risk of third-party plugins.
resource "sonarqube_setting" "plugin_risk_consent" {
  key   = "sonar.plugins.risk.consent"
  value = "ACCEPTED"
}

# Installs a plugin from the SonarQube Marketplace, which only Community
# Build has; commercial editions install plugins by hand. The server must be
# restarted for the change to take effect; until then restart_required is
# true, and destroying the resource cancels the pending installation.
resource "sonarqube_plugin" "checkstyle" {
  key        = "checkstyle"
  depends_on = [sonarqube_setting.plugin_risk_consent]
}
