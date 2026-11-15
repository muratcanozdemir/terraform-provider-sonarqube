#!/usr/bin/env bash
# Runs the scenarios in examples/scenarios against a live server, with the
# provider built from this checkout:
#
#   scripts/examples-test.sh [ENV_FILE] [SCENARIO...]
#
# ENV_FILE (default .acc.env, relative to the repository) holds SONAR_HOST
# and SONAR_TOKEN. With no
# SCENARIO, every scenario runs. Scenarios needing a higher edition than the
# server's are skipped. For each scenario:
#   1. terraform test             (its tests/*.tftest.hcl)
#   2. apply, then plan with -detailed-exitcode: a second plan must be empty
#   3. destroy
# Step 2 runs only for scenarios with a tests/idempotency.tfvars file, which
# holds the variables to apply with. A scenario with a tests/run.sh script
# runs that script instead of steps 1-3.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ENV_FILE=${1:-.acc.env}
shift || true
[[ $ENV_FILE == /* ]] || ENV_FILE="$ROOT/$ENV_FILE"
set -a; . "$ENV_FILE"; set +a

# Minimum edition per scenario; unlisted scenarios run everywhere.
declare -A MIN_EDITION=([devops-integration]=developer [portfolios]=enterprise)
declare -A RANK=([community]=0 [developer]=1 [enterprise]=2 [datacenter]=3)

edition=$(curl -fsS -H "Authorization: Bearer $SONAR_TOKEN" "$SONAR_HOST/api/navigation/global" |
  sed -nE 's/.*"edition":"([a-z]+)".*/\1/p')
edition=${edition:-community}
echo "server $SONAR_HOST: $edition edition"

api() { curl -fsS -H "Authorization: Bearer $SONAR_TOKEN" "$@"; }

# Scenario tests name everything tftest-*. Remove what a failed run left
# behind, so that it cannot break the next one.
cleanup() {
  local q=tftest-
  # A run cut short can leave a scenario's object as the server default. A
  # default cannot be deleted, and a default permission template that grants
  # nobody anything hides every new project from its creator: hand the
  # defaults back to the built-in ones first.
  local tpl
  tpl=$(api "$SONAR_HOST/api/permissions/search_templates" |
    jq -r '(.defaultTemplates[] | select(.qualifier == "TRK") | .templateId) as $id | .permissionTemplates[] | select(.id == $id) | .name')
  if [[ $tpl == "$q"* ]]; then
    api -X POST "$SONAR_HOST/api/permissions/set_default_template" --data-urlencode "templateName=Default template" -d qualifier=TRK >/dev/null || true
  fi
  api "$SONAR_HOST/api/permissions/search_templates?q=$q" | jq -r ".permissionTemplates[] | select(.name | startswith(\"$q\")) | .name" | while read -r t; do
    api -X POST "$SONAR_HOST/api/permissions/delete_template" --data-urlencode "templateName=$t" >/dev/null || true
  done
  local gates
  gates=$(api "$SONAR_HOST/api/qualitygates/list")
  if jq -e ".qualitygates[] | select(.isDefault and (.name | startswith(\"$q\")))" <<<"$gates" >/dev/null; then
    api -X POST "$SONAR_HOST/api/qualitygates/set_as_default" \
      --data-urlencode "name=$(jq -r '[.qualitygates[] | select(.isBuiltIn)][0].name' <<<"$gates")" >/dev/null || true
  fi
  api "$SONAR_HOST/api/qualityprofiles/search" | jq -r "
    .profiles as \$all | .profiles[] | select(.isDefault and (.name | startswith(\"$q\"))) | .language as \$l |
    [\$all[] | select(.language == \$l and .isBuiltIn)] | (map(select(.name == \"Sonar way\")) + .)[0] | [.language, .name] | @tsv" |
    while IFS=$'\t' read -r lang name; do
      api -X POST "$SONAR_HOST/api/qualityprofiles/set_default" -d "language=$lang" --data-urlencode "qualityProfile=$name" >/dev/null || true
    done
  api -X POST "$SONAR_HOST/api/projects/bulk_delete" -d "q=$q" -d qualifiers=TRK,APP,VW >/dev/null || true
  api "$SONAR_HOST/api/user_groups/search?q=$q&ps=500" | jq -r '.groups[].name' | while read -r g; do
    api -X POST "$SONAR_HOST/api/user_groups/delete" --data-urlencode "name=$g" >/dev/null || true
  done
  api "$SONAR_HOST/api/users/search?q=$q&ps=500" | jq -r '.users[].login' | while read -r u; do
    api -X POST "$SONAR_HOST/api/users/deactivate" --data-urlencode "login=$u" -d anonymize=true >/dev/null || true
  done
  api "$SONAR_HOST/api/qualitygates/list" | jq -r ".qualitygates[] | select(.name | startswith(\"$q\")) | .name" | while read -r g; do
    api -X POST "$SONAR_HOST/api/qualitygates/destroy" --data-urlencode "name=$g" >/dev/null || true
  done
  api "$SONAR_HOST/api/alm_settings/list_definitions" | jq -r '[.[] | arrays | .[].key] | .[]' | grep "^$q" | while read -r k; do
    api -X POST "$SONAR_HOST/api/alm_settings/delete" --data-urlencode "key=$k" >/dev/null || true
  done
  # Children first: a profile with descendants cannot be deleted.
  for _ in 1 2 3; do
    api "$SONAR_HOST/api/qualityprofiles/search" | jq -r ".profiles[] | select(.name | startswith(\"$q\")) | [.language, .name] | @tsv" |
      while IFS=$'\t' read -r lang name; do
        api -X POST "$SONAR_HOST/api/qualityprofiles/delete" -d "language=$lang" --data-urlencode "qualityProfile=$name" >/dev/null 2>&1 || true
      done
  done
}

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
GOBIN="$WORK/bin" go install -C "$ROOT" .
cat > "$WORK/terraformrc" <<RC
provider_installation {
  dev_overrides {
    "muratcanozdemir/sonarqube" = "$WORK/bin"
  }
  direct {}
}
RC
export TF_CLI_CONFIG_FILE="$WORK/terraformrc" TF_IN_AUTOMATION=1

cd "$ROOT/examples/scenarios"
scenarios=("$@")
if ((${#scenarios[@]} == 0)); then
  for d in */tests; do scenarios+=("${d%/tests}"); done
  # terraform query needs Terraform 1.14.
  if ! terraform version -json | jq -e '.terraform_version | split(".") | (.[0] | tonumber) > 1 or (.[1] | tonumber) >= 14' >/dev/null; then
    scenarios=("${scenarios[@]/bulk-import/}")
  fi
fi

failed=()
for s in "${scenarios[@]}"; do
  [[ -n $s ]] || continue
  need=${MIN_EDITION[$s]:-community}
  if ((RANK[$edition] < RANK[$need])); then
    echo "=== SKIP $s (needs $need)"
    continue
  fi
  echo "=== RUN  $s"
  cleanup || true # best effort
  # The subshell enables -e itself: errexit is ignored in a subshell run as
  # an if/&&/|| condition, which would let failing steps pass.
  set +e
  (
    set -e
    cd "$s"
    rm -rf .terraform .terraform.lock.hcl terraform.tfstate*
    terraform init -input=false -backend=false >/dev/null
    if [[ -x tests/run.sh ]]; then
      tests/run.sh
      exit 0
    fi
    terraform test
    if [[ -f tests/idempotency.tfvars ]]; then
      vars=(-input=false -var-file=tests/idempotency.tfvars)
      trap 'terraform destroy -auto-approve "${vars[@]}" >/dev/null' EXIT
      terraform apply -auto-approve "${vars[@]}" >/dev/null
      rc=0; terraform plan -detailed-exitcode "${vars[@]}" >"$WORK/plan.txt" || rc=$?
      if ((rc != 0)); then
        cat "$WORK/plan.txt"
        echo "second plan is not empty (exit $rc)"
        exit 1
      fi
      echo "idempotent: second plan is empty"
    fi
  )
  rc=$?
  set -e
  if ((rc != 0)); then failed+=("$s"); echo "--- FAIL $s"; else echo "--- PASS $s"; fi
  rm -rf "$s/.terraform" "$s/.terraform.lock.hcl" "$s"/terraform.tfstate*
done

((${#failed[@]} == 0)) || { echo "failed: ${failed[*]}"; exit 1; }
