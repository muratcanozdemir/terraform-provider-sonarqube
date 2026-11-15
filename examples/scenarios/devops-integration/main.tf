# GitHub integration:
#   - one GitHub App configured in SonarQube (the "DevOps platform" setting)
#   - pull request decoration: every project bound to its repository,
#     monorepos detected from the catalogue
#   - optionally, users log in with GitHub and land in groups named after
#     their GitHub teams
# GitHub App secrets are write-only: never in state or plan files.

resource "sonarqube_alm_github" "this" {
  key       = "${var.prefix}-github"
  url       = var.github_app.api_url
  app_id    = var.github_app.app_id
  client_id = var.github_app.client_id

  client_secret_wo       = var.github_app_client_secret
  private_key_wo         = var.github_app_private_key
  credentials_wo_version = var.credentials_version
}

locals {
  # A repository holding several projects is a monorepo.
  projects_per_repo = { for k, p in var.repositories : p.repository => k... }
}

module "project" {
  source   = "../modules/project"
  for_each = var.repositories

  key  = "${var.prefix}-${each.key}"
  name = each.value.name

  github = {
    alm_setting = sonarqube_alm_github.this.key
    repository  = each.value.repository
    monorepo    = length(local.projects_per_repo[each.value.repository]) > 1
  }
}

resource "sonarqube_github_configuration" "login" {
  count = var.github_login == null ? 0 : 1

  enabled        = var.github_login.enabled
  application_id = var.github_app.app_id
  client_id      = var.github_app.client_id
  api_url        = var.github_app.api_url
  web_url        = var.github_app.web_url

  client_secret_wo       = var.github_app_client_secret
  private_key_wo         = var.github_app_private_key
  credentials_wo_version = var.credentials_version

  # JIT: users are created at their first login. AUTO_PROVISIONING would
  # also synchronize users, teams and project permissions from GitHub.
  provisioning_type     = "JIT"
  allowed_organizations = var.github_login.organizations
  synchronize_groups    = var.github_login.synchronize_groups
}
