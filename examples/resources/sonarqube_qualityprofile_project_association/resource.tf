resource "sonarqube_qualityprofile_project_association" "api" {
  project         = "example-api"
  language        = "java"
  quality_profile = "Company Java"
}
