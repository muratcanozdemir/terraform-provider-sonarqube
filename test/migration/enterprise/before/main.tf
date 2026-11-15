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

resource "sonarqube_project" "p" {
  for_each = toset(["mig-ee-a", "mig-ee-b", "mig-ee-c", "mig-ee-d"])
  name     = each.key
  project  = each.key
  tags     = ["mig"]
}

resource "sonarqube_application" "app" {
  key         = "mig-ee-app"
  name        = "Mig app"
  description = "Migration test"
  projects    = [sonarqube_project.p["mig-ee-a"].project, sonarqube_project.p["mig-ee-b"].project]
}

resource "sonarqube_portfolio" "tags" {
  key            = "mig-ee-pf-tags"
  name           = "Mig tags"
  description    = "By tag"
  selection_mode = "TAGS"
  tags           = ["mig"]
}

resource "sonarqube_portfolio" "manual" {
  key            = "mig-ee-pf-manual"
  name           = "Mig manual"
  description    = "Manual"
  selection_mode = "MANUAL"
  selected_projects {
    project_key = sonarqube_project.p["mig-ee-a"].project
  }
}

resource "sonarqube_alm_github" "gh" {
  key            = "mig-ee-github"
  url            = "https://api.github.com"
  app_id         = "123456"
  client_id      = "Iv1.mig"
  client_secret  = "mig-secret"
  private_key    = "mig-key"
  webhook_secret = "mig-webhook"
}

resource "sonarqube_alm_gitlab" "gl" {
  key                   = "mig-ee-gitlab"
  url                   = "https://gitlab.example.com/api/v4"
  personal_access_token = "mig-pat"
}

resource "sonarqube_alm_azure" "az" {
  key                   = "mig-ee-azure"
  url                   = "https://dev.azure.com/mig"
  personal_access_token = "mig-pat"
}

resource "sonarqube_alm_bitbucket" "bb" {
  key                   = "mig-ee-bitbucket"
  url                   = "https://bitbucket.example.com"
  personal_access_token = "mig-pat"
}

resource "sonarqube_github_binding" "a" {
  project                 = sonarqube_project.p["mig-ee-a"].project
  alm_setting             = sonarqube_alm_github.gh.key
  repository              = "example/mig-a"
  summary_comment_enabled = true
}

resource "sonarqube_gitlab_binding" "b" {
  project     = sonarqube_project.p["mig-ee-b"].project
  alm_setting = sonarqube_alm_gitlab.gl.key
  repository  = "1234"
}

resource "sonarqube_azure_binding" "c" {
  project         = sonarqube_project.p["mig-ee-c"].project
  alm_setting     = sonarqube_alm_azure.az.key
  project_name    = "Mig"
  repository_name = "mig-c"
}

resource "sonarqube_bitbucket_binding" "d" {
  project     = sonarqube_project.p["mig-ee-d"].project
  alm_setting = sonarqube_alm_bitbucket.bb.key
  repository  = "MIG"
  slug        = "mig-d"
  monorepo    = false
}
