# 0005. Access tokens are cached between runs

## Context

Each command would otherwise log in to the controller first. That is an extra
request per command and, on a system where the secret is in an encrypted
file, a passphrase prompt per command.

## Decision

After a login the access token and its expiry time are saved, and the next
run uses them:

1. The API client asks for a token that is valid for at least another minute:
   first the one in memory, then the cached one, then a fresh login.
2. If the controller rejects a token anyway, the client forgets it, logs in
   again, and repeats the request once.
3. The client secret is read only when a login is actually needed.

Where the token is kept follows the host's secret backend: the keychain for
`keyring`; an owner-only file in `$XDG_RUNTIME_DIR/gomada/` for `file` (that
directory lives in memory and is wiped at logout); nowhere for `env`. The
client ID is stored with the token, so a token is never used after the host's
credentials changed.

The interactive view uses the same cache and writes back renewed tokens. When
its token is rejected because a command logged in meanwhile, it picks up the
newer cached token before logging in itself.

## Consequences

- With a valid token, a command needs neither a login nor the passphrase.
- Tokens never reach the config file or a disk that survives a reboot.
- `--no-token-cache` or `no_token_cache: true` turns the cache off.
