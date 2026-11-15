# A release gate: SonarQube decides whether a release may proceed.
#
# Put this in the configuration that deploys (or tags, or promotes) a release.
# The deployment depends on terraform_data.release_gate, whose preconditions
# fail the plan when a component's quality gate is not green or coverage is too
# low, so nothing ships. check blocks report softer problems as warnings.

data "sonarqube_server" "this" {}

data "sonarqube_quality_gate_status" "component" {
  for_each = var.release.components

  project = each.value
  branch  = var.release.branch
}

data "sonarqube_measures" "component" {
  for_each = var.release.components

  component   = each.value
  branch      = var.release.branch
  metric_keys = ["coverage", "ncloc", "violations", "security_hotspots", "duplicated_lines_density"]
}

locals {
  gates = { for k, g in data.sonarqube_quality_gate_status.component : k => g.status }

  failing = {
    for k, g in data.sonarqube_quality_gate_status.component : k => [
      for c in g.conditions : "${c.metric} = ${c.actual_value} (threshold ${c.error_threshold})" if c.status == "ERROR"
    ] if g.status != "OK"
  }

  coverage = { for k, m in data.sonarqube_measures.component : k => tonumber(lookup(m.values, "coverage", "0")) }

  # major.minor as one comparable number: 2025.1 -> 2025001, 9.9 -> 9009.
  version_number = { for k, v in {
    server  = data.sonarqube_server.this.version
    minimum = var.minimum_server_version
  } : k => tonumber(split(".", v)[0]) * 1000 + tonumber(split(".", v)[1]) }
}

resource "terraform_data" "release_gate" {
  input = var.release.version

  lifecycle {
    precondition {
      condition     = length(local.failing) == 0
      error_message = "Quality gate not passed, release ${var.release.version} blocked:\n${join("\n", [for k, f in local.failing : "  ${k} (${local.gates[k]}): ${length(f) > 0 ? join(", ", f) : "not analyzed"}"])}"
    }
    precondition {
      condition     = alltrue([for k, c in local.coverage : c >= var.minimum_coverage])
      error_message = "Coverage below ${var.minimum_coverage}%: ${join(", ", [for k, c in local.coverage : "${k} ${c}%" if c < var.minimum_coverage])}."
    }
  }
}

# Warnings: reported on every plan, never block.

check "server_supported" {
  assert {
    condition     = data.sonarqube_server.this.status == "UP"
    error_message = "SonarQube is ${data.sonarqube_server.this.status}; gate results may be stale."
  }
  assert {
    condition     = local.version_number.server >= local.version_number.minimum
    error_message = "SonarQube ${data.sonarqube_server.this.version} is older than ${var.minimum_server_version}."
  }
}

check "gate_conditions_relaxed" {
  assert {
    condition     = !anytrue([for g in data.sonarqube_quality_gate_status.component : g.ignored_conditions])
    error_message = "Coverage and duplication conditions were ignored for a component, because little code changed."
  }
}
