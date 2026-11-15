# Installs a plugin from the SonarQube Marketplace. The server must be
# restarted for the change to take effect.
resource "sonarqube_plugin" "checkstyle" {
  key = "checkstyle"
}
