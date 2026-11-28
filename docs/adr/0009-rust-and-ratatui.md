# 0009. gomada is written in Rust; the view is drawn with ratatui

## Context

gomada was first written in Go, with Cobra for the command line and Bubble
Tea for the interactive view. It was rewritten in Rust. A rewrite is only
worth having if nobody who uses the program has to notice it.

## Decision

The Rust version is a port, not a redesign. It keeps what people and scripts
depend on:

- the commands, flags, tables, JSON field names, and exit codes;
- the config file, the encrypted secret file, the keychain entries, and the
  cached token, byte for byte, so either version can read what the other
  wrote;
- the screens and keys of the interactive view (see [0006](0006-simple-tui.md)).

The expected output and screens of the Go version stayed in the repository as
the golden files of the Rust tests. Where the two differ, the difference is
listed in [rust-rewrite.html](../rust-rewrite.html).

The interactive view is drawn with ratatui on crossterm. ratatui gives the
terminal handling, the frame, and the styled lines; the table that scrolls
sideways stays gomada's own component, because ratatui's table shrinks
columns to fit where gomada cuts them off at the edge and scrolls.

Requests are blocking and run on threads, one per piece of background work,
which report back to the UI through a channel. There is no async runtime in
gomada's own code: a terminal tool that waits for one controller does not
need one.

TLS is rustls with the system's root certificates. A pinned certificate is
checked by a verifier of gomada's own, which still verifies the handshake
signature, so the peer must hold the key of the certificate it shows.

## Consequences

- One executable without a runtime; it starts faster and is smaller.
- Shared state is checked by the compiler: what crosses a thread is `Send`,
  what is shared is behind an `Arc` or a `Mutex`. The Go version needed the
  race detector for that.
- The closures the Go view stored for "what happens on enter" became enums
  (`PromptFor`, `ConfirmFor`, `ActionKind`), so every prompt and action is
  listed in one place.
- Cross-compiling needs a toolchain per target (`cross` in the release
  workflow), where Go needed nothing. The `ring` cryptography crate contains
  C and assembly.
- More, smaller dependencies than before: about 235 crates against 43 Go
  modules, most of them behind the Secret Service client and TLS.
- Usage errors are worded by clap, not Cobra. Scripts see the same exit
  code, 2.
