# Portfolios require the Enterprise edition or higher.
resource "sonarqube_portfolio" "payments" {
  key            = "payments"
  name           = "Payments"
  description    = "Every project tagged payments."
  selection_mode = "TAGS"
  tags           = ["payments"]
}

# A portfolio of portfolios and application branches.
resource "sonarqube_portfolio" "company" {
  key          = "company"
  name         = "Company"
  portfolios   = [sonarqube_portfolio.payments.key]
  applications = [sonarqube_application.shop.key]
  application_branches = {
    (sonarqube_application.shop.key) = [sonarqube_application_branch.release.name]
  }
}
