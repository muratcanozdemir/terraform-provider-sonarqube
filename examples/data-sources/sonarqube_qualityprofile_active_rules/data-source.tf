data "sonarqube_qualityprofile" "java" {
  name     = "Sonar way core"
  language = "java"
}

data "sonarqube_qualityprofile_active_rules" "java" {
  key = data.sonarqube_qualityprofile.java.key
}
