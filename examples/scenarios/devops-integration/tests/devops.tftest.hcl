# Needs Developer edition or above. The GitHub App is fictitious: SonarQube
# stores the settings without contacting GitHub, and login stays disabled.

variables {
  prefix = "tftest-dop"
  github_app = {
    app_id    = "123456"
    client_id = "Iv1.0123456789abcdef"
  }
  github_app_client_secret = "client-secret-v1"
  github_app_private_key   = <<-EOT
    -----BEGIN RSA PRIVATE KEY-----
    MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
    KUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQJAIJLixBy2qpFoS4DSmoEm
    -----END RSA PRIVATE KEY-----
  EOT
  github_login = {
    enabled       = false
    organizations = ["acme"]
  }
  repositories = {
    api      = { name = "API", repository = "acme/platform" }
    web      = { name = "Web", repository = "acme/platform" }
    payments = { name = "Payments", repository = "acme/payments" }
  }
}

run "bind" {
  assert {
    condition     = length(output.projects) == 3
    error_message = "every repository project is created"
  }
  assert {
    condition     = module.project["api"].key == "tftest-dop-api"
    error_message = "keys carry the prefix"
  }
  assert {
    condition     = sonarqube_github_configuration.login[0].allowed_organizations == toset(["acme"])
    error_message = "login is limited to the organization"
  }
}

# Rotating the GitHub App key: new values, new version, nothing in state.
run "rotate_app_secrets" {
  variables {
    github_app_client_secret = "client-secret-v2"
    credentials_version      = "2"
  }

  assert {
    condition     = sonarqube_alm_github.this.credentials_wo_version == "2"
    error_message = "the new secrets are sent"
  }
}
