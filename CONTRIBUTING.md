# Contributing

Thanks for helping. This page covers what you need to send a change.

## Before you open a pull request

```sh
make check
```

It runs what CI runs: the linter, the tests with the race detector, the
coverage floor, and a cross-compile for Windows and macOS. You need Go (the
version in `go.mod`) and [golangci-lint](https://golangci-lint.run) v2.

## Commit messages decide the version

Every merge to `main` is released automatically when its commits call for it.
The version comes from the [Conventional Commits](https://www.conventionalcommits.org)
prefix of each commit, so the prefix matters:

| Prefix | Release |
|---|---|
| `fix:`, `perf:` | Patch: 0.3.1 becomes 0.3.2 |
| `feat:` | Minor: 0.3.1 becomes 0.4.0 |
| `feat!:`, `fix!:`, or a `BREAKING CHANGE:` footer | Major (minor while below 1.0.0) |
| `docs:`, `test:`, `chore:`, `ci:`, `refactor:` | None |

If pull requests are squash-merged, the pull request title becomes the commit
message, so give it a prefix too.

## Layout

```
cmd/gomada/           main: calls cli.Execute and nothing else
internal/
  cli/                Cobra commands, one file per noun
  tui/                Bubble Tea model: screens, key handling, add-host wizard
    table/            the scrolling table every screen is built on
  app/                use cases shared by cli and tui
  omada/              Open API client: login, token renewal, paging, errors
    omadatest/        fake controller for tests
  config/             the config file
  secret/             client secret storage: keychain, encrypted file, env
  tokencache/         access token storage between runs
  output/             tables, JSON, and value formatting
  qr/                 QR codes drawn with text characters
  version/            build information
docs/adr/             decisions and their reasons
scripts/              release version and coverage scripts, with tests
```

Rules that keep it simple:

- `cli` and `tui` never call the API client. They call `app`, so a feature is
  written once and behaves the same in both.
- `omada` knows nothing about config files, keychains, or terminals.
- The TUI never blocks. Requests run as Bubble Tea commands and come back as
  messages.
- A new action on a device or client is one `action` value in
  `internal/tui/views.go` and one command in `internal/cli`. Confirmation and
  `--yes` handling come with them.
- A new tab is one `tab` value in `internal/tui/views.go`, listed in `New`.

## Tests

- Test against the fake controller in `omadatest`; extend it when you add an
  endpoint.
- Output and screens are compared with golden files in `testdata`. After an
  intended change run `make golden` and review the diff like code.
- To try the read-only calls against a real controller, see
  `internal/omada/integration_test.go`.

## Decisions

A change to how something fundamental works (storage, security, layout,
release) comes with a short record in `docs/adr`. Copy the shape of an
existing one.
