# Migration test: applied with jdamata/sonarqube, then planned with this
# provider after `terraform state replace-provider`. See scripts/migration-test.sh.
terraform {
  required_providers {
    sonarqube = {
      source  = "jdamata/sonarqube"
      version = "0.16.29"
    }
  }
}

variable "host" {}
variable "token" { sensitive = true }

provider "sonarqube" {
  host  = var.host
  token = var.token
}

resource "sonarqube_user" "alice" {
  login_name = "mig-alice"
  name       = "Alice"
  email      = "mig-alice@example.com"
  password   = "Mig-Alice-Passw0rd!"
}

resource "sonarqube_user" "ldap" {
  login_name = "mig-ldap"
  name       = "LDAP user"
  is_local   = false
}

resource "sonarqube_user_external_identity" "ldap" {
  login_name        = sonarqube_user.ldap.login_name
  external_provider = "LDAP_default"
  external_identity = "mig-ldap"
}

resource "sonarqube_user_token" "alice" {
  login_name = sonarqube_user.alice.login_name
  name       = "mig-token"
}

resource "sonarqube_group" "devs" {
  name        = "mig-devs"
  description = "Migration test group"
}

resource "sonarqube_group_member" "alice" {
  name       = sonarqube_group.devs.name
  login_name = sonarqube_user.alice.login_name
}

resource "sonarqube_project" "api" {
  name       = "Mig API"
  project    = "mig-api"
  visibility = "private"
  tags       = ["backend", "payments"]

  setting {
    key    = "sonar.exclusions"
    values = ["**/generated/**", "**/vendor/**"]
  }
}

resource "sonarqube_project_links" "docs" {
  project_key = sonarqube_project.api.project
  name        = "Docs"
  url         = "https://docs.example.com"
}

resource "sonarqube_project_main_branch" "api" {
  project = sonarqube_project.api.project
  name    = "trunk"
}

resource "sonarqube_new_code_periods" "api" {
  project = sonarqube_project.api.project
  type    = "NUMBER_OF_DAYS"
  value   = "14"
}

resource "sonarqube_qualitygate" "gate" {
  name = "mig-gate"
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

resource "sonarqube_qualitygate_project_association" "api" {
  gatename   = sonarqube_qualitygate.gate.name
  projectkey = sonarqube_project.api.project
}

resource "sonarqube_qualitygate_usergroup_association" "devs" {
  gatename   = sonarqube_qualitygate.gate.name
  group_name = sonarqube_group.devs.name
}

resource "sonarqube_qualityprofile" "base" {
  name     = "mig-base"
  language = "java"
}

resource "sonarqube_qualityprofile" "child" {
  name     = "mig-child"
  language = "java"
  parent   = sonarqube_qualityprofile.base.name
}

resource "sonarqube_qualityprofile_activate_rule" "complexity" {
  key      = sonarqube_qualityprofile.base.key
  rule     = "java:S3776"
  severity = "CRITICAL"
  params   = "Threshold=10"
}

resource "sonarqube_qualityprofile_activate_rule" "todo" {
  key  = sonarqube_qualityprofile.base.key
  rule = "java:S1135"
}

resource "sonarqube_qualityprofile_deactivate_rule" "todo" {
  key        = sonarqube_qualityprofile.child.key
  rule       = "java:S1135"
  depends_on = [sonarqube_qualityprofile_activate_rule.todo]
}

resource "sonarqube_qualityprofile_project_association" "api" {
  project         = sonarqube_project.api.project
  language        = "java"
  quality_profile = sonarqube_qualityprofile.child.name
}

resource "sonarqube_qualityprofile_usergroup_association" "devs" {
  language     = "java"
  profile_name = sonarqube_qualityprofile.base.name
  group_name   = sonarqube_group.devs.name
}

resource "sonarqube_rule" "custom" {
  custom_key           = "mig_no_legacy"
  template_key         = "java:S124"
  name                 = "Mig: no legacy client"
  markdown_description = "Use the v2 client."
  severity             = "MAJOR"
  type                 = "CODE_SMELL"
  params               = "regularExpression=legacy;message=Use the v2 client"
}

resource "sonarqube_setting" "base_url" {
  key   = "sonar.core.serverBaseURL"
  value = "https://mig.example.com"
}

resource "sonarqube_setting" "issue_ignore" {
  key = "sonar.issue.ignore.multicriteria"
  field_values = [
    { ruleKey = "java:S1135", resourceKey = "**/legacy/**" },
  ]
}

resource "sonarqube_secured_setting" "gh" {
  key   = "sonar.auth.github.clientSecret.secured"
  value = "mig-secret"
}

resource "sonarqube_permission_template" "tpl" {
  name                = "mig-template"
  description         = "Migration test"
  project_key_pattern = "mig-tpl-.*"
}

resource "sonarqube_permissions" "group_global" {
  group_name  = sonarqube_group.devs.name
  permissions = ["scan", "provisioning"]
}

resource "sonarqube_permissions" "user_project" {
  login_name  = sonarqube_user.alice.login_name
  project_key = sonarqube_project.api.project
  permissions = ["admin", "issueadmin"]
}

resource "sonarqube_permissions" "template_group" {
  group_name  = sonarqube_group.devs.name
  template_id = sonarqube_permission_template.tpl.id
  permissions = ["codeviewer", "user"]
}

resource "sonarqube_permissions" "template_creator" {
  special_group_name = "project_creator"
  template_name      = sonarqube_permission_template.tpl.name
  permissions        = ["admin"]
}

resource "sonarqube_webhook" "global" {
  name   = "mig-global"
  url    = "https://ci.example.com/hook"
  secret = "0123456789abcdef-mig"
}

resource "sonarqube_webhook" "project" {
  name    = "mig-project"
  url     = "https://ci.example.com/project-hook"
  project = sonarqube_project.api.project
}

resource "sonarqube_alm_gitlab" "gl" {
  key                   = "mig-gitlab"
  url                   = "https://gitlab.example.com/api/v4"
  personal_access_token = "mig-pat"
}

resource "sonarqube_alm_github" "gh" {
  key            = "mig-github"
  url            = "https://api.github.com"
  app_id         = "123456"
  client_id      = "Iv1.mig"
  client_secret  = "mig-secret"
  private_key    = "mig-key"
  webhook_secret = "mig-webhook"
}

resource "sonarqube_alm_azure" "az" {
  key                   = "mig-azure"
  url                   = "https://dev.azure.com/mig"
  personal_access_token = "mig-pat"
}

resource "sonarqube_alm_bitbucket" "bb" {
  key                   = "mig-bitbucket"
  url                   = "https://bitbucket.example.com"
  personal_access_token = "mig-pat"
}
