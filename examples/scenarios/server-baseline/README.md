# Scenario: server baseline

Server-wide governance in one configuration, for a new SonarQube or to bring
an existing one under Terraform:

| Concern | Resources |
|---|---|
| Who administers SonarQube | `sonarqube_group`, global `sonarqube_permissions` |
| What new projects look like | `sonarqube_permission_template` (default) and its `sonarqube_permissions` |
| The bar code must meet | `sonarqube_qualitygate` (default), `sonarqube_qualitygate_usergroup_association` |
| How "new code" is measured | global `sonarqube_new_code_periods` |
| Branch housekeeping | `sonarqube_setting` |
| Notifications | `sonarqube_webhook`, `sonarqube_email_configuration` |

The SMTP password and webhook secret are
[write-only](https://developer.hashicorp.com/terraform/language/resources/ephemeral#write-only-arguments):
they are sent to SonarQube but never stored in state or plan files. To rotate
them, change the values and bump `secrets_version`.

```sh
export SONAR_HOST=https://sonarqube.example.com SONAR_TOKEN=...   # an admin token
terraform init
terraform apply \
  -var 'webhook_url=https://hooks.example.com/sonarqube' \
  -var "webhook_secret=$WEBHOOK_SECRET"
```

Destroying the configuration resets the defaults to the built-in gate and
template. Pair it with [`../project-onboarding`](../project-onboarding), passing
the `groups` and `quality_gate` outputs to the project module.

Works on every edition. Requires Terraform 1.11+.
