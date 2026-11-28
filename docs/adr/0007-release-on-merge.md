# 0007. Every merge to main can release

## Context

Releases should not depend on someone remembering to tag.

## Decision

A workflow runs on every push to `main`. It runs the same checks as a pull
request, then `scripts/next-version.sh` reads the Conventional Commits
prefixes since the last tag and prints the next version, or nothing. If there
is a version, the workflow tags the commit, creates the release, and builds
and uploads executables for Linux, macOS, and Windows: the Linux ones with
`cross` on a Linux runner, the others on a runner of their own system.

Tagging and building happen in one workflow, because a tag pushed by a
workflow does not start other workflows. The workflow needs no secrets beyond
the token GitHub provides. Actions are pinned to commit hashes.

CI lints and tests on Linux, and runs the tests on macOS and Windows too.

## Consequences

- Commit prefixes are part of the interface: `feat:` and `fix:` release,
  `docs:` and `chore:` do not.
- A merge with only non-releasing commits publishes nothing.
- The executables are not signed.
