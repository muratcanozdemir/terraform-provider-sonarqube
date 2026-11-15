# Scenario: GitHub integration

- Configures one **GitHub App** in SonarQube (`sonarqube_alm_github`).
- Creates a project per entry of `repositories` with
  [`../modules/project`](../modules/project) and **binds** it to its repository
  for pull request decoration. Several projects in one repository are bound as a
  **monorepo** automatically.
- Optionally lets members of your GitHub organizations **log in** with GitHub
  (`sonarqube_github_configuration`), landing in groups named after their teams.

The App's client secret and private key are
[write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments):
pass them from a secret manager through ephemeral variables. After rotating the
key in GitHub, bump `credentials_version`.

```hcl
# terraform.tfvars
github_app   = { app_id = "123456", client_id = "Iv1.0123456789abcdef" }
github_login = { organizations = ["acme"] }
repositories = {
  platform-api = { name = "Platform API", repository = "acme/platform" }
  platform-web = { name = "Platform Web", repository = "acme/platform" }
  payments     = { name = "Payments", repository = "acme/payments" }
}
```

```sh
terraform apply \
  -var "github_app_client_secret=$GITHUB_APP_SECRET" \
  -var "github_app_private_key=$(cat app.private-key.pem)"
```

Needs **Developer edition** or above (bindings). The same pattern works for
GitLab, Azure DevOps and Bitbucket with `sonarqube_alm_*` and
`sonarqube_*_binding`.
