data "sonarqube_project_branches" "api" {
  project = "example-api"
}

# Every analyzed release branch, e.g. for an application branch.
output "release_branches" {
  value = [for b in data.sonarqube_project_branches.api.branches : b.name if startswith(b.name, "release/")]
}
