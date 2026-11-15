# A custom rule created from a template rule.
resource "sonarqube_rule" "no_legacy_import" {
  custom_key           = "no_legacy_import"
  template_key         = "java:S124"
  name                 = "Do not use the legacy client"
  markdown_description = "Use `com.example.client.v2` instead."
  severity             = "MAJOR"
  type                 = "CODE_SMELL"
  params               = "regularExpression=com\\.example\\.legacy\\.;message=Use the v2 client"
}
