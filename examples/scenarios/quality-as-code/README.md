# Scenario: quality profiles as code

```
built-in default ──▶ "<prefix> Java" ──▶ "<prefix> Java legacy"
                     + tuned rules        - exemptions
                     + a custom rule
```

- The company profile **inherits** the server's default Java profile (looked
  up with `sonarqube_qualityprofiles`, since built-in names differ between
  SonarQube versions), so rules added by upgrades arrive without a change here.
- `rule_overrides` activates rules and tunes their **severity and parameters**.
- A **custom rule** made from a rule template flags comments that reference
  a retired ticket system, and is activated in the company profile.
- A **child profile** relaxes a few rules for a legacy codebase.
- A group may **edit both profiles** without the global "Administer Quality
  Profiles" permission.

```sh
export SONAR_HOST=https://sonarqube.example.com SONAR_TOKEN=...
terraform init
terraform apply
```

Assign the profiles to projects with
`sonarqube_qualityprofile_project_association`, or through the `quality_profiles`
input of [`../modules/project`](../modules/project).

To keep a profile that was tuned in the UI, export it with *Back up* and
manage it with the `backup` argument of `sonarqube_qualityprofile` instead of
one resource per rule.

Works on every edition.
