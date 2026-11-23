# 0002. The client secret goes to the system keychain

## Context

The client secret gives access to network equipment. A plain config file is
the easy place for it and the wrong one: config files get copied, backed up,
and committed.

## Decision

The secret is stored through one small interface, `secret.Store`, with three
backends:

| Backend | Used when |
|---|---|
| `keyring` | Default. Keychain on macOS, Credential Manager on Windows, Secret Service on Linux. |
| `file` | No keychain is available (servers, SSH). The secret is encrypted with a key derived from a passphrase: scrypt, then AES-256-GCM. The host name is authenticated, so a file cannot be moved to another host. |
| `env` | Scripts and CI. Nothing is stored; `GOMADA_CLIENT_SECRET` is read on each run. |

The config file records only which backend a host uses. The secret is never a
command-line flag and never logged. `GOMADA_CLIENT_SECRET`, when set, wins
over whatever is stored.

## Consequences

- The config file is safe to back up or keep in a dotfiles repository.
- On a system without a keychain, a passphrase has to be typed. The token
  cache (0005) keeps that to about once every two hours.
- No backend protects against someone who already controls the user's
  account on a running machine. That is true of every keychain as well.
