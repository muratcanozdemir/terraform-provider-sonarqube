resource "sonarqube_qualityprofile" "java" {
  name     = "Company Java"
  language = "java"
}

resource "sonarqube_qualityprofile_activate_rule" "cognitive_complexity" {
  key      = sonarqube_qualityprofile.java.key
  rule     = "java:S3776"
  severity = "CRITICAL"
  params   = "Threshold=10"
}
