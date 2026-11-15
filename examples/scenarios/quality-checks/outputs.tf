output "gates" {
  description = "Quality gate status of each component: OK, ERROR, or NONE when not analyzed."
  value       = local.gates
}

output "report" {
  description = "Key measures of each component, e.g. for release notes."
  value       = { for k, m in data.sonarqube_measures.component : k => m.values }
}

output "server" {
  description = "SonarQube edition and version that judged the release."
  value       = "${data.sonarqube_server.this.edition} ${data.sonarqube_server.this.version}"
}
