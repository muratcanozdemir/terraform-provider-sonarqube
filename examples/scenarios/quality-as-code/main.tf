# Quality profiles as code:
#
#   built-in default ──▶ "<prefix> Java" ──▶ "<prefix> Java legacy"
#                        + tuned rules        - a few exemptions
#                        + a custom rule
#
# The company profile inherits the server's default Java profile, so rules
# added by SonarQube upgrades arrive without a change here. A child profile
# relaxes it for a legacy codebase. Editing is delegated to a group, without
# the global "Administer Quality Profiles" permission.

data "sonarqube_qualityprofiles" "java" {
  language = "java"
}

locals {
  # Built-in profile names differ between SonarQube versions; look it up.
  java_default = one([for p in data.sonarqube_qualityprofiles.java.quality_profiles : p.name if p.is_default && p.is_built_in])
}

resource "sonarqube_qualityprofile" "java" {
  name       = "${var.prefix} Java"
  language   = "java"
  parent     = local.java_default
  is_default = var.make_default
}

resource "sonarqube_qualityprofile_activate_rule" "java" {
  for_each = var.rule_overrides

  key      = sonarqube_qualityprofile.java.key
  rule     = each.key
  severity = each.value.severity
  params   = each.value.params
}

# A custom rule from a template: flag comments that reference an internal
# ticket system scheduled for retirement.
resource "sonarqube_rule" "legacy_tracker" {
  template_key         = "java:S124" # "Track comments matching a regular expression"
  custom_key           = "${var.prefix}-legacy-tracker"
  name                 = "${var.prefix}: do not reference LEGACY tickets"
  markdown_description = <<-EOT
    The LEGACY tracker is read-only since 2026. Move the ticket to the new
    tracker and update the comment.
  EOT
  params               = "regularExpression=.*LEGACY-[0-9]+.*;message=Move this LEGACY ticket to the new tracker"
  severity             = "MINOR"
  type                 = "CODE_SMELL"
}

resource "sonarqube_qualityprofile_activate_rule" "legacy_tracker" {
  key  = sonarqube_qualityprofile.java.key
  rule = sonarqube_rule.legacy_tracker.id
}

# Legacy team profile ------------------------------------------------------------

resource "sonarqube_qualityprofile" "java_legacy" {
  name     = "${var.prefix} Java legacy"
  language = "java"
  parent   = sonarqube_qualityprofile.java.name
}

resource "sonarqube_qualityprofile_deactivate_rule" "legacy" {
  for_each = var.legacy_exemptions

  key  = sonarqube_qualityprofile.java_legacy.key
  rule = each.value

  # The rule must be active in the parent before the child can switch it off.
  depends_on = [sonarqube_qualityprofile_activate_rule.java]
}

# Delegation ----------------------------------------------------------------------

resource "sonarqube_group" "java_guild" {
  name        = "${var.prefix}-java-guild"
  description = "Maintains the Java quality profiles."
}

resource "sonarqube_qualityprofile_usergroup_association" "java_guild" {
  for_each = {
    company = sonarqube_qualityprofile.java.name
    legacy  = sonarqube_qualityprofile.java_legacy.name
  }

  profile_name = each.value
  language     = "java"
  group_name   = sonarqube_group.java_guild.name
}
