#!/usr/bin/env bash
# Refuses a release whose code has not passed the full acceptance tier.
#
# Usage: scripts/release-gate.sh TAG
# Needs GH_TOKEN (actions: read) and GITHUB_REPOSITORY, in a checkout that
# has TAG.
#
# It looks for an "Acceptance [full]" run (see acceptance.yml) of a commit
# with the same tree as TAG: the release pull request's run normally, since
# merging it produces the same tree, or a manual run on the tag itself. While
# such a run is still in progress it waits, up to GATE_TIMEOUT seconds.
set -euo pipefail

tag=${1:?usage: release-gate.sh TAG}
tree=$(git rev-parse "$tag^{tree}")
deadline=$((SECONDS + ${GATE_TIMEOUT:-5400}))

while :; do
  runs=$(gh api "repos/$GITHUB_REPOSITORY/actions/workflows/acceptance.yml/runs?per_page=100" \
    --jq "[.workflow_runs[] | select(.display_title == \"Acceptance [full]\" and .head_commit.tree_id == \"$tree\")
           | {id, status, conclusion, html_url}]")
  if ok=$(jq -er 'first(.[] | select(.conclusion == "success")) | .html_url' <<< "$runs"); then
    echo "Full acceptance tier passed for $tag: $ok"
    exit 0
  fi
  if ! jq -e 'any(.[]; .status != "completed")' <<< "$runs" > /dev/null; then
    echo "::error::No passing full acceptance run for the code of $tag (tree $tree)." \
      "Run the Acceptance workflow on $tag with the default inputs (Actions > Acceptance > Run workflow)," \
      "then re-run this job."
    jq -r '.[] | "  \(.conclusion // .status): \(.html_url)"' <<< "$runs"
    exit 1
  fi
  if ((SECONDS >= deadline)); then
    echo "::error::Timed out waiting for the full acceptance run of $tag."
    exit 1
  fi
  echo "Full acceptance run for $tag still in progress; waiting."
  sleep 60
done
