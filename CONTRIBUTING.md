# Contributing

## Build and unit tests

```sh
make build   # ./terraform-provider-sonarqube
make test    # unit tests; acceptance tests skip without TF_ACC
make lint
```

## Code style

Write Go for the version in `go.mod`, using its current idioms: `new(v)` for a
pointer to a value, `slices`/`maps` helpers over hand-written loops,
`strings.Cut`/`SplitSeq`, `errors.AsType`, `sync.WaitGroup.Go`. `make lint`
enables the `modernize` linter, which flags the older forms; `go fix ./...`
rewrites most of them. Run it after raising the Go version.

Split import IDs with `splitID` (in `common.go`) rather than by hand, so every
resource reports a malformed ID the same way.

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

### What CI runs

The Acceptance workflow picks a tier per run (the tier is in the run name):

| Tier | When | Tests | SonarQube |
|------|------|-------|-----------|
| `pr` | pull request | only those the change needs | oldest + newest Community Build, Enterprise |
| `main` | push to main | all | oldest + newest Community Build, Enterprise |
| `full` | release pull request, weekly, manual dispatch | all, plus the jdamata migration job | every supported Community Build, Enterprise |

A pull request's tests come from `scripts/acc-test-map.txt`: each changed
file selects tests, shared code (`common.go`, `provider.go`, `go.mod`, ...)
selects all of them, and docs or release tooling select none. A file the map
does not list selects all tests, so a new source file only needs a line there
to narrow its pull requests. `make acc-select` shows what the current branch
would run. Require the **Acceptance result** check: it reports on every pull
request, also when no test job runs.

Releases are gated: `release.yml` builds nothing until a `full` run has
passed on the same code (`scripts/release-gate.sh`). release-please
dispatches that run on its pull request. For a tag pushed by hand, run the
Acceptance workflow on the tag first.

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
