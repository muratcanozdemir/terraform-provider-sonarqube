output "alm_setting" {
  description = "Key of the GitHub setting, for more bindings."
  value       = sonarqube_alm_github.this.key
}

output "projects" {
  description = "Bound project keys."
  value       = { for k, m in module.project : k => m.key }
}
