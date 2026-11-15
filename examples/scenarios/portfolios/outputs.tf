output "portfolios" {
  description = "Portfolio keys: the company, then one per business unit."
  value = {
    company = sonarqube_portfolio.company.key
    units   = { for k, p in sonarqube_portfolio.unit : k => p.key }
  }
}

output "applications" {
  description = "Application keys, by product."
  value       = { for k, a in sonarqube_application.product : k => a.key }
}
