data "sonarqube_qualityprofile" "java" {
  name     = "Sonar way core"
  language = "java"
}

# Rules of the language that the profile does not activate.
data "sonarqube_qualityprofile_deactivated_rules" "java" {
  key = data.sonarqube_qualityprofile.java.key
}
