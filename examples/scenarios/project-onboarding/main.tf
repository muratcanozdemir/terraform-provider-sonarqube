# Onboard every project of a YAML catalogue: one group per team plus one
# for its leads, a company quality gate, and per
# project the settings of ../modules/project.

locals {
  catalog = yamldecode(file("${path.module}/${var.catalog_file}"))

  # Flatten teams -> projects into one map keyed by project.
  projects = merge([
    for team, t in local.catalog.teams : {
      for key, p in t.projects : key => merge(p, { team = team })
    }
  ]...)

  leads = merge([
    for team, t in local.catalog.teams : { for login in try(t.leads, []) : "${team}/${login}" => { team = team, login = login } }
  ]...)
}

# Teams --------------------------------------------------------------------

resource "sonarqube_group" "team" {
  for_each = local.catalog.teams

  name        = "${var.prefix}-${each.key}"
  description = "Members of the ${each.key} team."
}

resource "sonarqube_group" "leads" {
  for_each = local.catalog.teams

  name        = "${var.prefix}-${each.key}-leads"
  description = "Leads of the ${each.key} team: administer its projects."
}

resource "sonarqube_user" "lead" {
  for_each = local.leads

  login_name = "${var.prefix}-${each.value.login}"
  name       = title(each.value.login)
  email      = "${each.value.login}@example.com"
  # Users log in through the identity provider; SonarQube holds no password.
  is_local = false
}

resource "sonarqube_group_member" "lead" {
  for_each = local.leads

  name       = sonarqube_group.leads[each.value.team].name
  login_name = sonarqube_user.lead[each.key].login_name
}

# Quality ------------------------------------------------------------------

resource "sonarqube_qualitygate" "standard" {
  name = "${var.prefix} standard"

  condition {
    metric    = "new_violations"
    op        = "GT"
    threshold = "0"
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

# A company profile per language, inheriting the server's current default so
# it picks up new rules on upgrades. Built-in profile names differ between
# SonarQube versions, hence the lookup.
data "sonarqube_qualityprofiles" "js" {
  language = "js"
}

resource "sonarqube_qualityprofile" "js" {
  name     = "${var.prefix} JavaScript"
  language = "js"
  parent   = one([for p in data.sonarqube_qualityprofiles.js.quality_profiles : p.name if p.is_default])
}

locals {
  company_profiles = {
    js = sonarqube_qualityprofile.js.name
  }
}

# Projects -----------------------------------------------------------------

module "project" {
  source   = "../modules/project"
  for_each = local.projects

  key        = "${var.prefix}-${each.key}"
  name       = each.value.name
  visibility = try(each.value.visibility, "private")
  tags       = [each.value.team]

  main_branch      = try(each.value.main_branch, null)
  exclusions       = try(each.value.exclusions, [])
  links            = try(each.value.links, {})
  quality_gate     = try(each.value.quality_gate, sonarqube_qualitygate.standard.name)
  quality_profiles = { for l in try(each.value.languages, []) : l => local.company_profiles[l] }
  new_code         = try(each.value.new_code, { type = "PREVIOUS_VERSION" })

  admin_groups  = [sonarqube_group.leads[each.value.team].name]
  member_groups = [sonarqube_group.team[each.value.team].name]

  # Rotated 14 days before the 90-day expiry, at the first apply in that window.
  analysis_token = {}
}
