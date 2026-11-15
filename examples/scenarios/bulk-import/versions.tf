terraform {
  # List resources and terraform query need Terraform 1.14.
  required_version = ">= 1.14"

  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

# host and token come from SONAR_HOST and SONAR_TOKEN.
provider "sonarqube" {}

variable "search" {
  description = "Adopt only objects whose key, login or name contains this text. Empty adopts everything."
  type        = string
  default     = ""
}
