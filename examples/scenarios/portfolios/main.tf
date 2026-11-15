# Reporting structure for an Enterprise edition:
#
#   portfolio "<prefix>"                         (the whole company)
#   ├── portfolio "<prefix>-<unit>" ...          (one per business unit,
#   │                                             projects selected by tag)
#   └── application branches of each product
#
#   application "<prefix>-<product>" ...         (one per product: its projects
#                                                 released and gated together)
#
# Business units select their projects by tag, so a project tagged by its
# team (see ../modules/project) shows up in the right portfolio without
# touching the portfolio.

locals {
  units    = toset([for p in var.projects : p.unit])
  products = toset([for p in var.projects : p.product])
}

resource "sonarqube_project" "this" {
  for_each = var.projects

  project = "${var.prefix}-${each.key}"
  name    = each.value.name
  tags    = ["unit-${each.value.unit}", "product-${each.value.product}"]
}

# Applications: one per product ---------------------------------------------

resource "sonarqube_application" "product" {
  for_each = local.products

  key         = "${var.prefix}-${each.key}"
  name        = title(each.key)
  description = "Projects that ship together as ${each.key}."
  projects    = [for k, p in var.projects : sonarqube_project.this[k].project if p.product == each.key]
  tags        = ["product-${each.key}"]
}

# The application's "release" branch: main branches of its projects today;
# point a project at its release branch once that branch has been analyzed.
resource "sonarqube_application_branch" "release" {
  for_each = local.products

  application = sonarqube_application.product[each.key].key
  name        = "release"
  projects    = { for p in sonarqube_application.product[each.key].projects : p => "" }
}

# Portfolios -------------------------------------------------------------------

resource "sonarqube_portfolio" "unit" {
  for_each = local.units

  key            = "${var.prefix}-${each.key}"
  name           = "${title(each.key)} unit"
  description    = "Every project tagged unit-${each.key}."
  selection_mode = "TAGS"
  tags           = ["unit-${each.key}"]

  # Tags are set on the projects; select after they exist.
  depends_on = [sonarqube_project.this]
}

resource "sonarqube_portfolio" "company" {
  key         = var.prefix
  name        = "${title(var.prefix)} company"
  description = "All business units, and the release branch of each product."
  portfolios  = [for u in sonarqube_portfolio.unit : u.key]

  applications         = [for a in sonarqube_application.product : a.key]
  application_branches = { for k, b in sonarqube_application_branch.release : sonarqube_application.product[k].key => [b.name] }
}

# Everything else: projects outside any tagged unit, so nothing is missed.
resource "sonarqube_portfolio" "unassigned" {
  key            = "${var.prefix}-unassigned"
  name           = "Unassigned projects"
  description    = "Projects matching the prefix that no other portfolio selects."
  selection_mode = "REGEXP"
  regexp         = "${var.prefix}-.*"
}
