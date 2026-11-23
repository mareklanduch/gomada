#!/usr/bin/env bash
# Tests for next-version.sh. Each case builds a throwaway repository with the
# given tag and commits and checks the version the script prints.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
failed=0

# check NAME EXPECTED TAG COMMIT...   (TAG "" means no release yet)
check() {
  local name=$1 want=$2 tag=$3
  shift 3
  local dir
  dir=$(mktemp -d)
  (
    cd "$dir"
    git init -q
    git config user.name test
    git config user.email test@example.com
    git config commit.gpgsign false
    git config tag.gpgsign false
    git commit -q --allow-empty -m "chore: start"
    if [[ -n "$tag" ]]; then git tag "$tag"; fi
    for message in "$@"; do git commit -q --allow-empty -m "$message"; done
  )
  local got
  got=$(cd "$dir" && "$script")
  rm -rf "$dir"
  if [[ "$got" == "$want" ]]; then
    echo "ok    $name"
  else
    echo "FAIL  $name: got '$got', want '$want'"
    failed=1
  fi
}

check "first release from a feature"      v0.1.0 ""      "feat: add the device list"
check "first release from a fix"          v0.0.1 ""      "fix: handle empty sites"
check "nothing to release yet"            ""     ""      "docs: describe the plan"
check "fix bumps patch"                   v0.3.2 v0.3.1  "fix: retry on a rejected token"
check "perf bumps patch"                  v0.3.2 v0.3.1  "perf: fetch pages in one go"
check "feature bumps minor"               v0.4.0 v0.3.1  "feat: reboot devices"
check "feature wins over fix"             v0.4.0 v0.3.1  "fix: typo" "feat(tui): sort columns" "fix: another"
check "scope is allowed"                  v0.3.2 v0.3.1  "fix(cli): exit code for usage errors"
check "docs and chores release nothing"   ""     v0.3.1  "docs: readme" "chore: bump deps" "test: more cases" "ci: cache" "refactor: tidy"
check "no commits since the tag"          ""     v0.3.1
check "breaking stays below v1"           v0.4.0 v0.3.1  "feat!: rename the host command"
check "breaking bumps major from v1"      v2.0.0 v1.3.1  "fix(config)!: new file format"
check "breaking footer"                   v2.0.0 v1.3.1  "feat: new config
 
BREAKING CHANGE: the config file moved"
check "feature from v1"                   v1.4.0 v1.3.1  "feat: wifi networks"
check "merge commits are ignored"         ""     v1.3.1  "Merge pull request #7 from someone/branch"
check "a word like feature is no prefix"  ""     v1.3.1  "feature work in progress" "fixed a thing"
check "two-digit numbers"                 v0.10.0 v0.9.12 "feat: more"

exit $failed
