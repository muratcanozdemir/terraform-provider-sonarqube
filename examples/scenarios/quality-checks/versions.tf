terraform {
  required_version = ">= 1.5"

  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

# host and token come from SONAR_HOST and SONAR_TOKEN. Read-only: a token
# with Browse permission on the projects is enough.
provider "sonarqube" {}
