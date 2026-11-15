variables {
  prefix = "tftest-qac"
}

run "profiles" {
  assert {
    condition     = sonarqube_qualityprofile.java.parent != null
    error_message = "the company profile inherits the server default"
  }
  assert {
    condition     = sonarqube_qualityprofile.java_legacy.parent == "tftest-qac Java"
    error_message = "the legacy profile inherits the company profile"
  }
  assert {
    condition     = sonarqube_rule.legacy_tracker.id == "java:tftest-qac-legacy-tracker"
    error_message = "the custom rule lives in the java repository"
  }
}

# Read the result back through the data sources.
run "read_back" {
  module {
    source = "./tests/read-back"
  }

  variables {
    company_profile_key = run.profiles.profile_keys.company
    legacy_profile_key  = run.profiles.profile_keys.legacy
  }

  assert {
    condition     = contains(data.sonarqube_qualityprofile_active_rules.company.rules[*].key, "java:S3776")
    error_message = "tuned rule is active in the company profile"
  }
  assert {
    condition     = contains(data.sonarqube_qualityprofile_active_rules.company.rules[*].key, run.profiles.custom_rule)
    error_message = "custom rule is active in the company profile"
  }
  assert {
    condition     = !contains(data.sonarqube_qualityprofile_active_rules.legacy.rules[*].key, "java:S107")
    error_message = "the legacy profile switches the exempted rule off"
  }
  assert {
    condition     = contains(data.sonarqube_qualityprofile_active_rules.legacy.rules[*].key, "java:S3776")
    error_message = "the legacy profile keeps the other company rules"
  }
}
