resource "sonarqube_qualityprofile" "base" {
  name     = "Company Java"
  language = "java"
  parent   = "Sonar way core" # built-in profile names vary by SonarQube version
}

resource "sonarqube_qualityprofile" "legacy" {
  name     = "Company Java legacy"
  language = "java"
  parent   = sonarqube_qualityprofile.base.name
}

# Turns off, in the child profile only, a rule it inherits from its parent.
resource "sonarqube_qualityprofile_deactivate_rule" "no_todo" {
  key  = sonarqube_qualityprofile.legacy.key
  rule = "java:S1135"
}
