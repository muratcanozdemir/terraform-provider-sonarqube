data "sonarqube_quality_gate_status" "api" {
  project = "example-api"
  branch  = "release/2.4"
}

# Hold the deployment while the quality gate fails.
resource "terraform_data" "deploy" {
  lifecycle {
    precondition {
      condition     = data.sonarqube_quality_gate_status.api.status == "OK"
      error_message = "The quality gate of example-api release/2.4 is ${data.sonarqube_quality_gate_status.api.status}."
    }
  }
}
