output "profiles" {
  description = "Profile names, for sonarqube_qualityprofile_project_association."
  value = {
    company = sonarqube_qualityprofile.java.name
    legacy  = sonarqube_qualityprofile.java_legacy.name
  }
}

output "custom_rule" {
  description = "Key of the custom rule."
  value       = sonarqube_rule.legacy_tracker.id
}

output "profile_keys" {
  description = "Profile keys, for the quality profile data sources."
  value = {
    company = sonarqube_qualityprofile.java.key
    legacy  = sonarqube_qualityprofile.java_legacy.key
  }
}
