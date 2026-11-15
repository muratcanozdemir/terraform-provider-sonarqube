terraform {
  required_version = ">= 1.11"

  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

# host and token come from SONAR_HOST and SONAR_TOKEN.
provider "sonarqube" {}
