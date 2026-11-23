# Security

gomada handles credentials for network equipment, so reports are taken
seriously.

## Reporting a problem

Please do not open a public issue. Use GitHub's private reporting instead:
on the repository page, open **Security > Report a vulnerability**.

Include what you found, how to reproduce it, and the gomada version
(`gomada version`).

## What gomada does to protect you

- The client secret is stored in the system keychain, or in a
  passphrase-encrypted file where there is no keychain. It is never written to
  the config file, never accepted as a command-line flag, and never logged.
- Access tokens are kept in the keychain or in an in-memory runtime
  directory, never in the config file.
- A self-signed controller certificate is pinned by fingerprint. A changed
  certificate is refused.
- `--verbose` logs request methods, paths, and status codes only: no headers,
  no bodies, no query strings.

## Good practice

Give the Open API application in the controller the smallest role that covers
what you use gomada for. That limits the damage if the secret ever leaks.
