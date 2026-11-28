# 0001. One core, two front ends

## Context

gomada is both an interactive terminal UI and a command line for scripts. If
each talked to the controller on its own, every feature would be written
twice and the two would drift apart.

## Decision

Both front ends call the `app` package and never the API client. `app` holds
the use cases: add a host, connect, find a device by name, reboot it. The API
client in `omada` is a plain library that knows nothing about config files,
keychains, or terminals.

```
cli (clap)  ─┐
             ├─▶ app ─▶ omada   (HTTP client for the Open API)
tui (rata-  ─┘      ├─▶ config  (hosts file)
     tui)           ├─▶ secret  (keychain, encrypted file, env)
                    └─▶ tokencache
```

Everything is one crate that is not published as a library, so nothing
becomes public API by accident.

## Consequences

- A feature is written once and behaves the same in both front ends.
- Rules such as "a name that matches two devices is an error" live in one
  place.
- Front ends stay thin: parse input, call `app`, show the result.
