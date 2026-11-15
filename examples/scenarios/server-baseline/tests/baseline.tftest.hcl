# Applies the baseline to a real server (SONAR_HOST, SONAR_TOKEN) and checks
# it, then destroys it, which also restores the built-in defaults.

variables {
  prefix         = "tftest-base"
  webhook_url    = "https://hooks.example.com/sonarqube"
  webhook_secret = "0123456789abcdef-v1"
  smtp = {
    host     = "smtp.example.com"
    username = "sonarqube"
    from     = "sonarqube@example.com"
  }
  smtp_password = "smtp-v1"
}

run "baseline" {
  assert {
    condition     = sonarqube_qualitygate.default.is_default
    error_message = "the company gate is the default"
  }
  assert {
    condition     = length(sonarqube_qualitygate.default.condition) == 4
    error_message = "all four gate conditions are set"
  }
  assert {
    condition     = sonarqube_permission_template.default.default
    error_message = "the template applies to new projects"
  }
  assert {
    condition     = sonarqube_webhook.global[0].secret_wo_version == "1"
    error_message = "the webhook is signed"
  }
}

# New secrets only need a new version: their values never reach state.
run "rotate_secrets" {
  variables {
    webhook_secret  = "0123456789abcdef-v2"
    smtp_password   = "smtp-v2"
    secrets_version = "2"
  }

  assert {
    condition     = sonarqube_email_configuration.this[0].credentials_wo_version == "2"
    error_message = "the new SMTP password is sent"
  }
}
