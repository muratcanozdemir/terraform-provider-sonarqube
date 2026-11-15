terraform {
  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

# Token authentication (recommended). host and token can also come from the
# SONAR_HOST and SONAR_TOKEN environment variables.
provider "sonarqube" {
  host  = "https://sonarqube.example.com"
  token = var.sonarqube_token
}

variable "sonarqube_token" {
  type      = string
  sensitive = true
}
