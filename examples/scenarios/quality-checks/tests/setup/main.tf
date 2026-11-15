# Test helper: projects to release. They have never been analyzed, so their
# quality gate status is NONE.
terraform {
  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

variable "keys" {
  type = set(string)
}

resource "sonarqube_project" "p" {
  for_each = var.keys

  project = each.value
  name    = each.value
}
