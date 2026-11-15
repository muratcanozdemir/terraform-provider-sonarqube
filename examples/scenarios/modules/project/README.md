# Module: project

A SonarQube project with what a team needs on day one:

- visibility, tags, main branch name and analysis exclusions
- project permissions for an admin group and a member group
- quality gate, quality profiles per language and the new code definition
- project links
- a project analysis token for CI that rotates itself before it expires
  (owned by the account Terraform runs as: SonarQube issues analysis tokens only to the caller)
- optionally, a GitHub binding for pull request decoration (Developer edition or above)

```hcl
module "payments_api" {
  source = "../modules/project"

  key           = "acme-payments-api"
  name          = "Payments API"
  tags          = ["team-payments"]
  quality_gate  = "Acme way"
  admin_groups  = ["team-payments-leads"]
  member_groups = ["team-payments"]

  new_code       = { type = "REFERENCE_BRANCH", value = "main" }
  analysis_token = { expiration_days = 90, rotate_before_days = 14 }
}
```

Groups, gates and profiles must already exist. Create them in the same
configuration (see `../server-baseline`) or reference them by name.

The analysis token is stored in state, as the CI system needs to read it.
Treat the state as secret, or use the `sonarqube_user_token` ephemeral
resource to hand a short-lived token to a write-only argument instead.
