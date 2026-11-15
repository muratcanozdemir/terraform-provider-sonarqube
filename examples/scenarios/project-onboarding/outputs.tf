output "projects" {
  description = "Onboarded project keys, by catalogue entry."
  value       = { for k, m in module.project : k => m.key }
}

output "analysis_tokens" {
  description = "CI analysis token of each project. Push these to your CI secrets store."
  value       = { for k, m in module.project : k => m.analysis_token }
  sensitive   = true
}

output "token_expiry" {
  description = "When each analysis token expires; a regular apply rotates them 14 days before."
  value       = { for k, m in module.project : k => m.analysis_token_expires }
}
