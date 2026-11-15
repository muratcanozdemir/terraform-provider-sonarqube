data "sonarqube_server" "this" {}

output "sonarqube_version" {
  value = data.sonarqube_server.this.version
}
