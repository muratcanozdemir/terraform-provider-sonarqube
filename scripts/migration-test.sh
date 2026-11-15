#!/usr/bin/env bash
# End-to-end check of the migration path from jdamata/sonarqube:
#   1. apply test/migration/<edition>/before with jdamata/sonarqube
#   2. terraform state replace-provider to this provider
#   3. plan test/migration/<edition>/after with this provider: must be empty
#   4. destroy with this provider
# Usage: scripts/migration-test.sh community|enterprise
# Needs SONAR_HOST and SONAR_TOKEN (e.g. from .acc.env) and terraform on PATH.
set -euo pipefail

EDITION=${1:?usage: $0 community|enterprise}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SRC="$ROOT/test/migration/$EDITION"
[ -d "$SRC" ] || { echo "no such edition: $EDITION"; exit 2; }
: "${SONAR_HOST:?}" "${SONAR_TOKEN:?}"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
export TF_VAR_host=$SONAR_HOST TF_VAR_token=$SONAR_TOKEN TF_IN_AUTOMATION=1
tf() { (cd "$WORK/config" && terraform "$@" -no-color); }

# This provider, through a dev override (the published one is not used).
mkdir -p "$WORK/bin" "$WORK/config"
(cd "$ROOT" && go build -o "$WORK/bin/terraform-provider-sonarqube" .)
cat > "$WORK/dev.tfrc" <<TFRC
provider_installation {
  dev_overrides { "muratcanozdemir/sonarqube" = "$WORK/bin" }
  direct {}
}
TFRC

cleanup_on_failure() {
  echo "::group::destroying what is left"
  TF_CLI_CONFIG_FILE="$WORK/dev.tfrc" tf destroy -auto-approve || true
  echo "::endgroup::"
}

echo "== 1. apply with jdamata/sonarqube"
cp "$SRC/before/"*.tf "$WORK/config/"
tf init -input=false >/dev/null
tf apply -auto-approve -input=false

echo "== 2. replace provider"
rm "$WORK/config/"*.tf
cp "$SRC/after/"*.tf "$WORK/config/"
tf state replace-provider -auto-approve \
  registry.terraform.io/jdamata/sonarqube registry.terraform.io/muratcanozdemir/sonarqube

echo "== 3. plan with this provider (must be empty)"
export TF_CLI_CONFIG_FILE="$WORK/dev.tfrc"
set +e
tf plan -detailed-exitcode -input=false > "$WORK/plan.log" 2>&1
code=$?
set -e
cat "$WORK/plan.log"
if [ "$code" -ne 0 ]; then
  echo "::error::plan after migration is not empty (exit $code)"
  cleanup_on_failure
  exit 1
fi

echo "== 4. destroy with this provider"
tf destroy -auto-approve
echo "migration test passed ($EDITION)"
