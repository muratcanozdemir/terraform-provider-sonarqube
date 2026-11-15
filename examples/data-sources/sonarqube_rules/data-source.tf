data "sonarqube_rules" "java_bugs" {
  languages  = ["java"]
  types      = ["BUG"]
  severities = ["BLOCKER", "CRITICAL"]
}
