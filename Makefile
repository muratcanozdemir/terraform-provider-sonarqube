.PHONY: build install test testacc testacc-ee docs docs-check lint fmt acc-up acc-ee-up acc-down

GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
TFPLUGINDOCS  := go tool -modfile=tools/go.mod tfplugindocs
SQ_VERSION    ?= 26.11.0.134175
SQ_EE_VERSION ?= 2026.5.5

build:
	go build -o terraform-provider-sonarqube .

install: ## Install into GOBIN for a dev_overrides block (see CONTRIBUTING.md)
	go install .

test: ## Unit tests; acceptance tests skip without TF_ACC
	go test -race ./...

testacc: ## Acceptance tests against the Community Build server in .acc.env
	set -a; . ./.acc.env; set +a; TF_ACC=1 go test -count=1 -timeout 60m -v ./internal/provider/

testacc-ee: ## Acceptance tests against the Enterprise server in .acc-ee.env
	set -a; . ./.acc-ee.env; set +a; TF_ACC=1 go test -count=1 -timeout 60m -v ./internal/provider/

docs: ## Regenerate docs/ from the schema, templates/ and examples/
	$(TFPLUGINDOCS) generate --provider-name sonarqube --rendered-provider-name SonarQube

docs-check: docs
	$(TFPLUGINDOCS) validate --provider-name sonarqube
	git diff --exit-code -- docs || { echo "docs/ is out of date: run 'make docs'"; exit 1; }

fmt:
	gofmt -w .
	terraform fmt -recursive examples

lint:
	$(GOLANGCI_LINT) run ./...

acc-up: ## Start a Community Build server on :9100 and write .acc.env
	docker run -d --name sqtf-acc -p 9100:9000 -e SONAR_ES_BOOTSTRAP_CHECKS_DISABLE=true sonarqube:$(SQ_VERSION)-community
	SONAR_HOST=http://localhost:9100 ./scripts/acc-seed.sh .acc.env

acc-ee-up: ## Start an Enterprise server on :9200 (license in SONARQUBE_LICENSE) and write .acc-ee.env
	docker run -d --name sqtf-ee -p 9200:9000 -e SONAR_ES_BOOTSTRAP_CHECKS_DISABLE=true sonarqube:$(SQ_EE_VERSION)-enterprise
	SONAR_HOST=http://localhost:9200 ./scripts/acc-seed.sh .acc-ee.env

acc-down:
	docker rm -f sqtf-acc sqtf-ee
