# Test helper: reads a portfolio back through the data source.
terraform {
  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

variable "portfolio" {
  type = string
}

data "sonarqube_portfolio" "unit" {
  key = var.portfolio
}
