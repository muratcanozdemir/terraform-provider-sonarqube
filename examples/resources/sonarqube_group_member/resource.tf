resource "sonarqube_group" "developers" {
  name = "developers"
}

resource "sonarqube_group_member" "alice" {
  name       = sonarqube_group.developers.name
  login_name = "alice"
}
