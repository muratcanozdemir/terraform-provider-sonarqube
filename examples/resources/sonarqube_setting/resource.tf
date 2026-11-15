# A global single-value setting.
resource "sonarqube_setting" "base_url" {
  key   = "sonar.core.serverBaseURL"
  value = "https://sonarqube.example.com"
}

# A multi-value setting on one project.
resource "sonarqube_setting" "api_exclusions" {
  key       = "sonar.exclusions"
  component = "example-api"
  values    = ["**/generated/**"]
}

# A property-set setting.
resource "sonarqube_setting" "issue_ignore" {
  key = "sonar.issue.ignore.multicriteria"
  field_values = [
    { ruleKey = "java:S1135", resourceKey = "**/legacy/**" },
  ]
}
