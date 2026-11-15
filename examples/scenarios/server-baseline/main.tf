# Server-wide governance for a fresh (or existing) SonarQube:
#   - groups and global permissions: no one but the admin group administers
#   - a default permission template: new projects start private to their team
#   - the default quality gate, and who may edit it
#   - new code definition and branch housekeeping
#   - a global webhook and outgoing email, secrets kept out of state

# Groups and global permissions ---------------------------------------------

resource "sonarqube_group" "admins" {
  name        = "${var.prefix}-sonar-admins"
  description = "Administer SonarQube."
}

resource "sonarqube_group" "developers" {
  name        = "${var.prefix}-developers"
  description = "Every engineer. Projects are visible to them through the permission template."
}

resource "sonarqube_group" "quality_owners" {
  name        = "${var.prefix}-quality-owners"
  description = "Maintain the quality gate and profiles."
}

resource "sonarqube_permissions" "admins" {
  group_name  = sonarqube_group.admins.name
  permissions = ["admin", "gateadmin", "profileadmin", "provisioning"]
}

resource "sonarqube_permissions" "developers" {
  group_name  = sonarqube_group.developers.name
  permissions = ["provisioning"] # create projects; the template gives them the rest
}

# Permission template ---------------------------------------------------------

resource "sonarqube_permission_template" "default" {
  name        = "${var.prefix} default"
  description = "Applied to every new project: private to developers, administered by its creator."
  default     = var.make_defaults
}

resource "sonarqube_permissions" "template_developers" {
  template_name = sonarqube_permission_template.default.name
  group_name    = sonarqube_group.developers.name
  permissions   = ["user", "codeviewer", "issueadmin", "securityhotspotadmin"]
}

resource "sonarqube_permissions" "template_admins" {
  template_name = sonarqube_permission_template.default.name
  group_name    = sonarqube_group.admins.name
  permissions   = ["admin", "user", "codeviewer"]
}

resource "sonarqube_permissions" "template_creator" {
  template_name      = sonarqube_permission_template.default.name
  special_group_name = "project_creator"
  permissions        = ["admin"]
}

# Quality gate -------------------------------------------------------------------

resource "sonarqube_qualitygate" "default" {
  name       = "${var.prefix} way"
  is_default = var.make_defaults

  # "Clean as you code": hold new code to the bar, not the legacy.
  condition {
    metric    = "new_violations"
    op        = "GT"
    threshold = "0"
  }
  condition {
    metric    = "new_security_hotspots_reviewed"
    op        = "LT"
    threshold = "100"
  }
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
  condition {
    metric    = "new_duplicated_lines_density"
    op        = "GT"
    threshold = "3"
  }
}

# Delegate gate edits without granting global gateadmin.
resource "sonarqube_qualitygate_usergroup_association" "owners" {
  gatename   = sonarqube_qualitygate.default.name
  group_name = sonarqube_group.quality_owners.name
}

# New code and housekeeping ------------------------------------------------

resource "sonarqube_new_code_periods" "global" {
  type  = "NUMBER_OF_DAYS"
  value = "30"
}

resource "sonarqube_setting" "keep_branches" {
  key    = "sonar.dbcleaner.branchesToKeepWhenInactive"
  values = ["main", "master", "develop", "release/.*"]
}

resource "sonarqube_setting" "inactive_branch_days" {
  key   = "sonar.dbcleaner.daysBeforeDeletingInactiveBranchesAndPRs"
  value = "60"
}

# Notifications -----------------------------------------------------------------

resource "sonarqube_webhook" "global" {
  count = var.webhook_url == null ? 0 : 1

  name              = "${var.prefix} analysis events"
  url               = var.webhook_url
  secret_wo         = var.webhook_secret
  secret_wo_version = var.secrets_version
}

resource "sonarqube_email_configuration" "this" {
  count = var.smtp == null ? 0 : 1

  host              = var.smtp.host
  port              = var.smtp.port
  security_protocol = "STARTTLS"
  username          = var.smtp.username
  from_address      = var.smtp.from
  subject_prefix    = "[SonarQube]"

  basic_password_wo      = var.smtp_password
  credentials_wo_version = var.secrets_version
}
