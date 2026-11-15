# Scenario: portfolios and applications

A reporting structure for the Enterprise edition:

```
portfolio "acme"                       the whole company
├── portfolio "acme-commerce"          one per business unit, projects selected by tag
├── portfolio "acme-finance"
└── release branch of each application

application "acme-shop"                one per product: projects released together
application "acme-books"
portfolio "acme-unassigned"            REGEXP catch-all
```

Business-unit portfolios select projects **by tag**, so a project tagged by
its owners shows up in the right portfolio without editing the portfolio.
**Applications** group the projects of a product, and an **application
branch** pins which branch of each project makes up a release. The company
portfolio combines the units (as sub-portfolios) and each product's release
branch.

```hcl
# terraform.tfvars
projects = {
  checkout = { name = "Checkout", unit = "commerce", product = "shop" }
  catalog  = { name = "Catalog", unit = "commerce", product = "shop" }
  ledger   = { name = "Ledger", unit = "finance", product = "books" }
}
```

Portfolios are computed in the background, so new projects appear in them
after SonarQube's next portfolio refresh.

Needs **Enterprise edition** or above (applications need Developer).
