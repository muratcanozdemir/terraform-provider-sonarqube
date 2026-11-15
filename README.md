# Terraform Provider for SonarQube

Manage [SonarQube Server](https://www.sonarsource.com/products/sonarqube/) and
SonarQube Community Build with Terraform: projects, quality gates and quality
profiles, rules, permissions and permission templates, users, groups and
tokens, DevOps platform integrations and pull request decoration, applications
and their branches, portfolios, settings, webhooks and email notifications.

Documentation: [registry.terraform.io/providers/muratcanozdemir/sonarqube](https://registry.terraform.io/providers/muratcanozdemir/sonarqube/latest/docs)
(the same pages are in [`docs/`](docs/)).

## Usage

```hcl
terraform {
  required_providers {
    sonarqube = {
      source = "muratcanozdemir/sonarqube"
    }
  }
}

provider "sonarqube" {
  host = "https://sonarqube.example.com" # or SONAR_HOST
  # token from SONAR_TOKEN
}

resource "sonarqube_project" "api" {
  name    = "API"
  project = "example-api"
}

resource "sonarqube_qualitygate" "strict" {
  name = "Strict"

  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
}

resource "sonarqube_qualitygate_project_association" "api" {
  gatename   = sonarqube_qualitygate.strict.name
  projectkey = sonarqube_project.api.project
}
```

Every resource and data source has a working example under
[`examples/`](examples/).

Coming from [jdamata/sonarqube](https://registry.terraform.io/providers/jdamata/sonarqube)?
Resource names, arguments and state carry over; see the
[migration guide](docs/guides/migrating-from-jdamata.md).

## Highlights

- **Edition-aware plans.** Applications, bindings and portfolios are checked
  against the server's edition at plan time, so an unsupported configuration
  fails before anything changes.
- **Secrets stay out of state:** write-only user passwords, secured
  settings, webhook secrets, DevOps platform and SMTP credentials (Terraform
  1.11+), an ephemeral
  `sonarqube_user_token` (Terraform 1.10+), and `metadata_only` tokens that
  are tracked and revoked without storing their value.
- **Token rotation:** `sonarqube_user_token` can expire a set number of days
  after creation and be replaced a set number of days before it expires.
- **Safe under parallelism.** Quality profile hierarchy changes are
  serialized, because SonarQube deadlocks on concurrent ones.
- **Quality profiles as code:** manage a profile's rules from a backup file
  exported from the UI, with drift detection, or copy an existing profile.
  Unknown rules fail the apply instead of being skipped silently, as
  SonarQube's restore does.
- **Bring existing servers under Terraform:** `terraform query` lists
  projects, profiles, gates, groups and users and generates their
  configuration, and import blocks can use resource identity.
- **Actions** to send a test email and to re-apply permission templates to
  existing projects.
- **No silent destruction.** Deleting a quality profile that still has
  child profiles, or a DevOps platform setting that projects outside
  Terraform still use, is refused with an explanation.
- **Current APIs.** Built on the v2 Web API where SonarQube offers it, with no
  deprecated endpoints.
- **Acceptance-tested against real servers:** Community Build from 25.1 to
  the latest release, and the Enterprise edition.

## Requirements

- Terraform 1.5 or later (some features need newer versions, as noted in
  their documentation)
- SonarQube Community Build 25.1 or later, or SonarQube Server. The
  acceptance tests cover Community Build 25.1 to 26.11 and SonarQube Server
  Enterprise 2026.5.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
