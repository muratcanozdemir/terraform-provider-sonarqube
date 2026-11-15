data "sonarqube_measures" "api" {
  component   = "example-api"
  metric_keys = ["coverage", "bugs", "vulnerabilities"]
}
