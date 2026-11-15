# One SonarQube project with everything a team needs on day one: who can
# see and administer it, the quality gate and profiles it is held to, how
# "new code" is measured, and a CI token that rotates itself.

resource "sonarqube_project" "this" {
  project    = var.key
  name       = var.name
  visibility = var.visibility
  tags       = var.tags

  # With at least one block, the list is authoritative: settings changed in
  # the UI are reset at the next apply. No block leaves settings unmanaged.
  dynamic "setting" {
    for_each = length(var.exclusions) > 0 ? [1] : []
    content {
      key    = "sonar.exclusions"
      values = var.exclusions
    }
  }
}

resource "sonarqube_project_main_branch" "this" {
  count = var.main_branch == null ? 0 : 1

  project = sonarqube_project.this.project
  name    = var.main_branch
}

# Permissions --------------------------------------------------------------

locals {
  # Everyone can browse a public project and see its code; SonarQube refuses
  # to grant these two permissions on one.
  read = var.visibility == "public" ? [] : ["user", "codeviewer"]
}

resource "sonarqube_permissions" "admins" {
  for_each = var.admin_groups

  project_key = sonarqube_project.this.project
  group_name  = each.value
  permissions = concat(["admin", "issueadmin", "securityhotspotadmin"], local.read)
}

resource "sonarqube_permissions" "members" {
  for_each = length(local.read) == 0 ? toset([]) : setsubtract(var.member_groups, var.admin_groups)

  project_key = sonarqube_project.this.project
  group_name  = each.value
  permissions = ["user", "codeviewer"]
}

# Users that run analyses, such as a CI service account.
resource "sonarqube_permissions" "scanners" {
  for_each = var.scan_users

  project_key = sonarqube_project.this.project
  login_name  = each.value
  permissions = concat(["scan"], local.read)
}

# Quality ------------------------------------------------------------------

resource "sonarqube_qualitygate_project_association" "this" {
  count = var.quality_gate == null ? 0 : 1

  projectkey = sonarqube_project.this.project
  gatename   = var.quality_gate
}

resource "sonarqube_qualityprofile_project_association" "this" {
  for_each = var.quality_profiles

  project         = sonarqube_project.this.project
  language        = each.key
  quality_profile = each.value
}

resource "sonarqube_new_code_periods" "this" {
  count = var.new_code == null ? 0 : 1

  project = sonarqube_project.this.project
  type    = var.new_code.type
  value   = var.new_code.value
}

resource "sonarqube_project_links" "this" {
  for_each = var.links

  project_key = sonarqube_project.this.project
  name        = each.key
  url         = each.value
}

# CI access ----------------------------------------------------------------

resource "sonarqube_user_token" "analysis" {
  count = var.analysis_token == null ? 0 : 1

  name               = "ci-${var.key}"
  type               = "PROJECT_ANALYSIS_TOKEN"
  project_key        = sonarqube_project.this.project
  expiration_days    = var.analysis_token.expiration_days
  rotate_before_days = var.analysis_token.rotate_before_days
}

resource "sonarqube_github_binding" "this" {
  count = var.github == null ? 0 : 1

  project                 = sonarqube_project.this.project
  alm_setting             = var.github.alm_setting
  repository              = var.github.repository
  monorepo                = tostring(var.github.monorepo)
  summary_comment_enabled = tostring(var.github.summary_comment_enabled)
}
