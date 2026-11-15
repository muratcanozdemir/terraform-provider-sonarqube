#!/usr/bin/env bash
# Prepares a fresh SonarQube instance for the acceptance tests:
#   - waits until the server is UP
#   - replaces the default admin password
#   - installs a license from $SONARQUBE_LICENSE, if set (commercial editions)
#   - creates an admin token
# Writes SONAR_HOST and SONAR_TOKEN to the file named by $1 (default: .acc.env).
set -euo pipefail

URL=${SONAR_HOST:-http://localhost:9000}
PASS=${SONARQUBE_ADMIN_PASSWORD:-Acc-Admin-Passw0rd!}
OUT=${1:-.acc.env}

# poll CMD... retries CMD with exponential backoff (capped at 15s) until it
# succeeds or POLL_TIMEOUT seconds (default 600) have elapsed.
poll() {
  local delay=1 deadline=$((SECONDS + ${POLL_TIMEOUT:-600}))
  until "$@"; do
    if ((SECONDS + delay > deadline)); then return 1; fi
    sleep "$delay"
    delay=$((delay * 2 > 15 ? 15 : delay * 2))
  done
}

is_up() { curl -fsS "$URL/api/system/status" 2>/dev/null | grep -q '"status":"UP"'; }

echo "waiting for $URL"
poll is_up || { echo "server not UP"; exit 1; }

if curl -fsS -u admin:admin "$URL/api/authentication/validate" | grep -q '"valid":true'; then
  curl -fsS -u admin:admin -X POST "$URL/api/users/change_password" \
    --data-urlencode login=admin --data-urlencode previousPassword=admin --data-urlencode "password=$PASS"
fi

if [[ -n "${SONARQUBE_LICENSE:-}" ]]; then
  curl -fsS -u "admin:$PASS" -X POST "$URL/api/editions/set_license" --data-urlencode "license=$SONARQUBE_LICENSE"
fi

TOKEN=$(curl -fsS -u "admin:$PASS" -X POST "$URL/api/user_tokens/generate" \
  --data-urlencode "name=acc-$(date +%s%N)" | sed -E 's/.*"token":"([^"]+)".*/\1/')

umask 077
printf 'SONAR_HOST=%s\nSONAR_TOKEN=%s\n' "$URL" "$TOKEN" > "$OUT"
echo "wrote $OUT"
