#!/usr/bin/env bash
# Selects the acceptance tests a change needs, from scripts/acc-test-map.txt.
#
# Usage: scripts/acc-select.sh [BASE_REF]   (changed files: git diff BASE_REF...HEAD)
#        scripts/acc-select.sh - < files    (changed files, one per line)
#
# Prints, and appends to $GITHUB_OUTPUT when set:
#   mode=all|selected|none   whether the suite runs whole, in part or not at all
#   run=<regex>              go test -run argument when mode=selected
#   migration=true|false     whether the jdamata migration job is needed
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
map="$root/scripts/acc-test-map.txt"

if [[ "${1:-}" == - ]]; then
  mapfile -t files
else
  mapfile -t files < <(git -C "$root" diff --name-only "${1:-origin/main}...HEAD")
fi

# The map, without comments and blank lines: "glob selection" pairs.
globs=() sels=()
while read -r glob sel _; do
  [[ -z "$glob" || "$glob" == \#* ]] && continue
  globs+=("$glob") sels+=("$sel")
done < "$map"

lookup() { # file -> selection, or empty when no line matches
  local f=$1 i
  for i in "${!globs[@]}"; do
    # shellcheck disable=SC2053 # the glob is meant to match as a pattern
    if [[ "$f" == ${globs[$i]} ]]; then
      echo "${sels[$i]}"
      return
    fi
  done
}

all=false migration=false
declare -A tests=()
for f in "${files[@]}"; do
  [[ -z "$f" ]] && continue
  sel=$(lookup "$f")
  if [[ -z "$sel" && "$f" == internal/provider/*_test.go ]]; then
    # An unlisted test file selects the acceptance tests it defines; a
    # deleted one has nothing left to run.
    sel=NONE
    if [[ -f "$root/$f" ]]; then
      defined=$(sed -n 's/^func \(TestAcc[A-Za-z0-9_]*\)(t \*testing\.T).*/\1/p' "$root/$f" | paste -sd'|' -)
      [[ -n "$defined" ]] && sel="$defined"
    fi
  fi
  case "${sel:-ALL}" in
    ALL) all=true; echo "  $f: all tests" >&2 ;;
    NONE) ;;
    MIGRATION) migration=true; echo "  $f: migration job" >&2 ;;
    *) echo "  $f: $sel" >&2
       IFS='|' read -r -a names <<< "$sel"
       for n in "${names[@]}"; do tests[$n]=1; done ;;
  esac
done

if $all; then
  mode=all run='' migration=true
elif ((${#tests[@]})); then
  mode=selected
  run="^($(printf '%s\n' "${!tests[@]}" | sort | paste -sd'|' -))"
else
  mode=none run=''
fi

out="mode=$mode
run=$run
migration=$migration"
echo "$out"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then echo "$out" >> "$GITHUB_OUTPUT"; fi
