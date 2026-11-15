# Contributing

## Build and unit tests

```sh
make build   # ./terraform-provider-sonarqube
make test    # unit tests; acceptance tests skip without TF_ACC
make lint
```

## Acceptance tests

Acceptance tests run real Terraform against a real SonarQube server and need
Docker.

```sh
make acc-up     # Community Build on :9100, writes .acc.env
make testacc

# Enterprise edition (needs a license):
SONARQUBE_LICENSE=... make acc-ee-up   # :9200, writes .acc-ee.env
make testacc-ee

make acc-down
```

Tests that need a commercial edition skip themselves on Community Build, and
the other way round. Run one test with
`TF_ACC=1 go test ./internal/provider/ -run TestAccProject -v` after loading
an env file (`set -a; . ./.acc.env; set +a`).

`.acc.env` and `.acc-ee.env` hold an admin token and are git-ignored.

## Trying a local build

Point Terraform at your build with a `dev_overrides` block in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "muratcanozdemir/sonarqube" = "/path/to/your/GOBIN"
  }
  direct {}
}
```

then `make install`, and run `terraform plan` in a configuration (no
`terraform init` needed for the overridden provider).

## Documentation

`docs/` is generated: edit schema descriptions in Go, `templates/` or
`examples/`, then run `make docs`. CI fails when `docs/` is out of date.

## Commits and releases

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, ...). release-please turns them into the changelog and a
release pull request; merging it publishes the release.
