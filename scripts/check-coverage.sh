#!/usr/bin/env bash
# Runs the tests with the race detector and fails when a core package drops
# below the coverage floor. In GitHub Actions it also writes the numbers to
# the job summary, so they show on every pull request.
set -euo pipefail

floor=80
core='internal/(omada|config|secret|app)$'

# cmd/gomada has no tests of its own: it is one line that calls internal/cli.
out=$(go test -race -cover ./internal/... 2>&1) || { echo "$out"; exit 1; }
echo "$out"

report=$(awk '$1 == "ok" { for (i = 1; i <= NF; i++) if ($i == "coverage:") print $2, $(i + 1) }' <<<"$out")

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Test coverage"
    echo
    echo "| Package | Coverage |"
    echo "|---|---|"
    while read -r pkg pct; do echo "| \`$pkg\` | $pct |"; done <<<"$report"
  } >>"$GITHUB_STEP_SUMMARY"
fi

failed=0
seen=0
while read -r pkg pct; do
  [[ "$pkg" =~ $core ]] || continue
  seen=$((seen + 1))
  if awk -v p="${pct%\%}" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
    echo "coverage of $pkg is $pct, below the floor of $floor%"
    failed=1
  fi
done <<<"$report"

if [[ "$seen" -ne 4 ]]; then
  echo "expected coverage for 4 core packages, found $seen"
  exit 1
fi
exit $failed
