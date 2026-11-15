# Test helper: reads the profiles back through the data sources.
terraform {
  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

variable "company_profile_key" {
  type = string
}

variable "legacy_profile_key" {
  type = string
}

data "sonarqube_qualityprofile_active_rules" "company" {
  key = var.company_profile_key
}

data "sonarqube_qualityprofile_active_rules" "legacy" {
  key = var.legacy_profile_key
}
