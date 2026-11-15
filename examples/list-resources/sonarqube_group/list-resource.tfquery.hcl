list "sonarqube_group" "teams" {
  provider = sonarqube

  config {
    search = "team-"
  }
}
