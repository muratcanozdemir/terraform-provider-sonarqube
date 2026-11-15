resource "sonarqube_qualityprofile" "base" {
  name     = "Company Java"
  language = "java"
  parent   = "Sonar way core" # built-in profile names vary by SonarQube version
}

# Inherits every rule of the base profile.
resource "sonarqube_qualityprofile" "strict" {
  name       = "Company Java strict"
  language   = "java"
  parent     = sonarqube_qualityprofile.base.name
  is_default = true
}

# Rules managed as a backup (exported with "Back up" in the UI). Rules not in
# the file are deactivated; changes made in the UI show up as drift.
resource "sonarqube_qualityprofile" "python" {
  name     = "Company Python"
  language = "py"
  backup   = file("${path.module}/company-python.xml")
}

# A one-time copy, e.g. to start from a built-in profile and tune it with
# sonarqube_qualityprofile_activate_rule.
resource "sonarqube_qualityprofile" "python_experimental" {
  name      = "Company Python experimental"
  language  = "py"
  copy_from = sonarqube_qualityprofile.python.name
}
