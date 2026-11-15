# Scenario: release gate

Read-only. A deployment configuration asks SonarQube whether a release may
proceed:

- `sonarqube_quality_gate_status`: the gate of each component, on the
  release branch.
- `sonarqube_measures`: coverage and other measures, for an extra threshold
  and for release notes.
- `sonarqube_server`: the server is up and recent enough.

`terraform_data.release_gate` carries the blocking checks as preconditions:
make your deployment resources depend on it, and a red gate fails the plan
with the failed conditions listed:

```
│ Error: Resource precondition failed
│ Quality gate not passed, release 1.4.0 blocked:
│   acme-api (ERROR): new_coverage = 61.0 (threshold 80), new_violations = 3 (threshold 0)
```

`check` blocks report non-blocking problems as warnings.

```hcl
# terraform.tfvars
release = {
  version    = "1.4.0"
  components = ["acme-api", "acme-web"]
  branch     = "release/1.4"
}
minimum_coverage = 70
```

A token with *Browse* permission on the projects is enough. Works on every
edition; `branch` needs Developer edition or above.
