# 0007. Every merge to main can release

## Context

Releases should not depend on someone remembering to tag.

## Decision

A workflow runs on every push to `main`. It runs the same checks as a pull
request, then `scripts/next-version.sh` reads the Conventional Commits
prefixes since the last tag and prints the next version, or nothing. If there
is a version, the workflow tags the commit and GoReleaser builds and
publishes executables for Linux, macOS, and Windows from one Linux runner.

Tagging and building happen in one job, because a tag pushed by a workflow
does not start other workflows. The workflow needs no secrets beyond the
token GitHub provides. Actions are pinned to commit hashes.

CI runs on Linux only. It also builds and vets for Windows and macOS, which
catches code that would not compile there; it does not run the tests there.

## Consequences

- Commit prefixes are part of the interface: `feat:` and `fix:` release,
  `docs:` and `chore:` do not.
- A merge with only non-releasing commits publishes nothing.
- Platform-specific test failures on Windows or macOS would not be caught by
  CI. Test runners for them can be added if such bugs appear.
- The executables are not signed.
