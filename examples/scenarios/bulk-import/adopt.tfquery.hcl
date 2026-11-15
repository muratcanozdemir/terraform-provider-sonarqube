# Finds the objects to bring under Terraform. Run
#
#   terraform query -generate-config-out=generated.tf -var search=payments
#
# to write a resource block and an import block for each match.

list "sonarqube_project" "all" {
  provider         = sonarqube
  include_resource = true

  config {
    search = var.search
  }
}

list "sonarqube_group" "all" {
  provider         = sonarqube
  include_resource = true

  config {
    search = var.search
  }
}

# Gates and profiles have no search filter: every custom one is listed.
# Built-in ones cannot be managed and are skipped.
list "sonarqube_qualitygate" "all" {
  provider         = sonarqube
  include_resource = true
}

list "sonarqube_qualityprofile" "all" {
  provider         = sonarqube
  include_resource = true
}

list "sonarqube_user" "all" {
  provider         = sonarqube
  include_resource = true

  config {
    search = var.search
  }
}
