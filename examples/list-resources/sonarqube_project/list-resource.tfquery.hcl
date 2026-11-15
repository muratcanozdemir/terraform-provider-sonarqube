# terraform query -generate-config-out=projects.tf writes a resource and an
# import block for every match.
list "sonarqube_project" "payments" {
  provider         = sonarqube
  include_resource = true

  config {
    search = "payments"
  }
}
