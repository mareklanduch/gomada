# Contributing

Thanks for helping. This page covers what you need to send a change.

## Before you open a pull request

```sh
make check
```

It runs what CI runs: `cargo fmt --check`, `cargo clippy` with warnings as
errors, and the tests. You need a stable [Rust](https://rustup.rs) toolchain
with the `clippy` and `rustfmt` components.

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
src/
  main.rs             calls cli::execute and nothing else
  cli.rs              clap commands and what each one prints
  tui/                the interactive view, drawn with ratatui
    mod.rs            terminal setup, the event loop, messages
    model.rs          state, key handling, the add-host wizard
    views.rs          the lists, tabs, and actions
    render.rs         drawing: header, body, bottom line
    table.rs          the scrolling table every screen is built on
    text.rs           measuring, cutting, and wrapping text; the tab bar
  app.rs              use cases shared by cli and tui
  omada/              Open API client: login, token renewal, paging, TLS
  config.rs           the config file
  secret.rs           client secret storage: keychain, encrypted file, env
  tokencache.rs       access token storage between runs
  output.rs           tables, JSON, and value formatting
  qr.rs               QR codes drawn with text characters
  error.rs            the one error type and its classes
  version.rs          build information
tests/
  cli.rs, core.rs, env.rs   tests that run against the fake controller
  live.rs             read-only calls against a real controller (ignored)
  support/fake.rs     the fake controller
  testdata/           expected output and screens
docs/adr/             decisions and their reasons
scripts/              the release version script, with its test
```

Rules that keep it simple:

- `cli` and `tui` never call the API client. They call `app`, so a feature is
  written once and behaves the same in both.
- `omada` knows nothing about config files, keychains, or terminals.
- The TUI never blocks. Requests run as commands on their own threads and
  come back as messages.
- A new action on a device or client is one `ActionKind` in
  `src/tui/views.rs` and one command in `src/cli.rs`. Confirmation and
  `--yes` handling come with them.
- A new tab is one `Tab` value in `tabs()` in `src/tui/views.rs`.
- Errors are values of `error::Error`. Front ends ask an error for its class
  (`is_auth`, `is_unreachable`, ...); they never look at its text.

## Tests

- Test against the fake controller in `tests/support/fake.rs`; extend it when
  you add an endpoint.
- Output and screens are compared with golden files in `tests/testdata`.
  After an intended change run `make golden` and review the diff like code.
  The screens are drawn with ratatui's `TestBackend`, so no terminal is
  needed.
- Tests that set environment variables live in `tests/env.rs`, in one test,
  because the environment belongs to the whole process.
- To try the read-only calls against a real controller, see `tests/live.rs`.

## Decisions

A change to how something fundamental works (storage, security, layout,
release) comes with a short record in `docs/adr`. Copy the shape of an
existing one.
