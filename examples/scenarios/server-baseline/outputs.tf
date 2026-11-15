output "groups" {
  description = "Group names, for project modules to reference."
  value = {
    admins         = sonarqube_group.admins.name
    developers     = sonarqube_group.developers.name
    quality_owners = sonarqube_group.quality_owners.name
  }
}

output "quality_gate" {
  description = "Name of the company quality gate."
  value       = sonarqube_qualitygate.default.name
}
