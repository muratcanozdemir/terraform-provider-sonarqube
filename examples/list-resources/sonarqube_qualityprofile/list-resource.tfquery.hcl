list "sonarqube_qualityprofile" "java" {
  provider = sonarqube

  config {
    language = "java"
  }
}
