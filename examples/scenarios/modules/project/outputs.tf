output "key" {
  description = "Project key."
  value       = sonarqube_project.this.project
}

output "analysis_token" {
  description = "Project analysis token for CI (SONAR_TOKEN), or null. It changes when the token rotates."
  value       = one(sonarqube_user_token.analysis[*].token)
  sensitive   = true
}

output "analysis_token_expires" {
  description = "Expiration date of the analysis token, or null."
  value       = one(sonarqube_user_token.analysis[*].expiration_date)
}
