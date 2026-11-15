# Needs Enterprise edition or above.

variables {
  prefix = "tftest-pf"
  projects = {
    checkout = { name = "Checkout", unit = "commerce", product = "shop" }
    catalog  = { name = "Catalog", unit = "commerce", product = "shop" }
    ledger   = { name = "Ledger", unit = "finance", product = "books" }
  }
}

run "structure" {
  assert {
    condition     = length(output.portfolios.units) == 2
    error_message = "one portfolio per business unit"
  }
  assert {
    condition     = length(sonarqube_application.product["shop"].projects) == 2
    error_message = "the shop application holds both shop projects"
  }
  assert {
    condition     = length(sonarqube_portfolio.company.portfolios) == 2
    error_message = "the company portfolio includes every unit"
  }
}

run "read_back" {
  module {
    source = "./tests/read-back"
  }

  variables {
    portfolio = run.structure.portfolios.units["commerce"]
  }

  assert {
    condition     = data.sonarqube_portfolio.unit.selection_mode == "TAGS"
    error_message = "the unit portfolio selects by tag"
  }
}
