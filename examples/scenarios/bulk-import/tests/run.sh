#!/usr/bin/env bash
# Scenario test, run by scripts/examples-test.sh from this directory with
# the provider override in place. Creates objects outside Terraform, adopts
# them with terraform query, and checks that the generated configuration
# imports them without any change. Never destroys: the state is dropped and
# the test objects are deleted through the API by the runner's cleanup.
set -euo pipefail

api() { curl -fsS -H "Authorization: Bearer $SONAR_TOKEN" "$@" >/dev/null; }
q=tftest-imp
api -X POST "$SONAR_HOST/api/projects/create" -d "project=$q-api" -d "name=$q api" -d visibility=private
api -X POST "$SONAR_HOST/api/projects/create" -d "project=$q-web" -d "name=$q web" -d visibility=public
api -X POST "$SONAR_HOST/api/user_groups/create" -d "name=$q-team" -d "description=Adopted"
api -X POST "$SONAR_HOST/api/users/create" -d "login=$q-alice" -d "name=Alice" -d "email=alice@example.com" -d local=false
api -X POST "$SONAR_HOST/api/qualitygates/create" --data-urlencode "name=$q gate"
api -X POST "$SONAR_HOST/api/qualityprofiles/create" -d language=java --data-urlencode "name=$q java"

trap 'rm -f generated.tf terraform.tfstate*' EXIT
terraform query -var "search=$q" -generate-config-out=generated.tf >/dev/null

for want in "$q-api" "$q-web" "$q-team" "$q-alice" "$q gate" "$q java"; do
  grep -q -- "\"$want\"" generated.tf || { echo "generated.tf misses $want"; cat generated.tf; exit 1; }
done
echo "query found every object"

rc=0
terraform plan -no-color -var "search=$q" -detailed-exitcode -out=import.tfplan >plan.txt || rc=$?
summary=$(grep -E '^Plan:' plan.txt || true)
if ((rc == 1)) || [[ "$summary" != *" 0 to add, 0 to change, 0 to destroy."* ]]; then
  cat plan.txt
  echo "adopting must import without changes, got: ${summary:-exit $rc}"
  exit 1
fi
echo "$summary"
terraform apply -auto-approve import.tfplan >/dev/null
rm -f import.tfplan plan.txt

rc=0; terraform plan -no-color -var "search=$q" -detailed-exitcode >plan.txt || rc=$?
if ((rc != 0)); then cat plan.txt; echo "plan after import is not empty"; exit 1; fi
rm -f plan.txt
echo "idempotent: plan after import is empty"
