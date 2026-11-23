# gomada

A terminal app for TP-Link Omada controllers. One binary works as an
interactive view and as a scriptable command line, talking to the controller
through the Omada Open API.

```
home › Default  [Devices]  Clients                                           1/4
     NAME         TYPE     STATUS     MODEL      IP            UPTIME    CPU   MEM
▸ ●  Core router  gateway  connected  ER707-M2   192.168.0.1   41d 6h     8%   31%
  ●  Rack switch  switch   connected  SG3428XMP  192.168.0.2   41d 6h    12%   44%
  ○  AP garage    ap       offline    EAP225     192.168.0.23  -           -     -
  ●  AP office    ap       connected  EAP670     192.168.0.21  12d 2h     5%   52%

enter details · r reboot · n rename · ? help · / filter · tab clients · S sites
```

- See the devices and connected clients of a site.
- Reboot and rename devices. Rename, block, unblock, and reconnect clients.
- Keep several controllers, each with several sites.
- The client secret lives in the system keychain, never in a config file.
- Works on small screens: tables scroll sideways.

> **Status.** gomada is new. It is tested against a fake controller built from
> the Open API reference, and has not yet been confirmed against every
> controller version. Please report anything that behaves differently on yours.

## Install

Download the archive for your system from the
[releases page](https://github.com/mareklanduch/gomada/releases), unpack it,
and put `gomada` on your `PATH`. There are builds for Linux (amd64, arm64,
armv6, armv7), macOS (Intel and Apple silicon), and Windows (amd64, arm64).

The executables are not signed yet, so macOS and Windows warn about an
unidentified developer on first run.

With Go installed you can also build it yourself:

```sh
go install github.com/mareklanduch/gomada/cmd/gomada@latest
```

## Set up

gomada logs in as an Open API application, not as a person.

1. In the controller, open **Settings > Platform Integration > Open API** and
   add an application in **client credentials** mode. Give it a role that
   allows what you want to do; a read-only role is enough for looking.
2. Note the **client ID** and **client secret** it shows.
3. Run `gomada`. With no host set up yet it asks for the controller address,
   the client ID, and the client secret.

Or do the same from the command line:

```sh
gomada host add home --url https://192.168.0.2:8043 --client-id 9f8e7d
```

The client secret is never a command-line flag, because flags end up in shell
history. It is typed without echo, piped in with `--secret-stdin`, or taken
from the `GOMADA_CLIENT_SECRET` environment variable.

### Self-signed certificates

Most controllers use a self-signed certificate. gomada shows its fingerprint
when you add the host and, once you accept it, accepts only that certificate
from then on. If the certificate changes later, gomada refuses to connect and
says so. In scripts, pass the fingerprint with `--tls-pin sha256:...`.

## The interactive view

Run `gomada` with no arguments.

| Key | Does |
|---|---|
| `↑` `↓` or `k` `j` | Move |
| `←` `→` or `h` `l` | Scroll the table sideways; the name stays in place |
| `enter` | Open a host or site; show the details of a device or client |
| `tab` | Switch between Devices and Clients |
| `/` | Filter by text in any column |
| `o` | Sort by the next column |
| `r` `n` | Reboot or rename the selected device |
| `n` `b` `c` | Rename, block, or reconnect the selected client |
| `R` | Reload; the open tab also reloads every 10 seconds |
| `S` `H` | Sites of this host; all hosts (`a` adds one, `x` removes one) |
| `?` | All keys |
| `q` | Quit |

Rebooting, blocking, and removing ask for confirmation first.

## Commands

Everything the interactive view does is also a command.

```
gomada                          open the interactive view
gomada host   add | list | use | remove | test
gomada site   list | use
gomada device list | show | reboot | rename
gomada client list | show | rename | block | unblock | reconnect
gomada version
gomada completion bash | zsh | fish | powershell
```

```sh
gomada device list --type ap
gomada device reboot "AP office" --yes
gomada client list --wireless -o json | jq '.[].name'
gomada --host office --site Workshop client block aa:bb:cc:dd:ee:ff --yes
```

- Devices and clients are named by MAC address (in any notation) or by name.
  A name that matches several is an error that lists them.
- `-o json` prints stable JSON for scripts. Results go to standard output,
  messages and errors to standard error.
- `reboot`, `block`, and `host remove` ask for confirmation. Outside a
  terminal they refuse unless you pass `--yes`.
- A blocked client is offline, so `client unblock` takes its MAC address.

Global flags: `--host`, `--site`, `--output`, `--timeout`, `--yes`,
`--verbose`, `--no-color`, `--no-token-cache`, `--config`.

| Exit code | Meaning |
|---|---|
| 0 | Success |
| 1 | The command failed |
| 2 | Wrong usage |
| 3 | The controller refused the client ID and secret |
| 4 | The controller could not be reached, or its certificate was not accepted |

## Where things are stored

**Configuration**: the hosts and which one is current. It holds no secrets.

| System | File |
|---|---|
| Linux | `$XDG_CONFIG_HOME/gomada/config.yaml`, usually `~/.config/gomada/config.yaml` |
| macOS | `~/Library/Application Support/gomada/config.yaml` |
| Windows | `%AppData%\gomada\config.yaml` |

`GOMADA_CONFIG` or `--config` points to a different file.

**Client secret**: one per host.

| System | Where |
|---|---|
| macOS | Keychain |
| Windows | Credential Manager |
| Linux desktop | Secret Service (GNOME Keyring, KWallet, KeePassXC) |
| Linux without a keychain (servers, SSH) | A file next to the config, encrypted with a passphrase you choose (scrypt and AES-256-GCM). gomada asks for the passphrase when it needs the secret. |
| Scripts and CI | `--secret-store env`: nothing is stored; the secret is read from `GOMADA_CLIENT_SECRET` on each run |

**Access token**: the controller's token lasts about two hours. gomada keeps
it between runs so a command can skip the login, renews it a minute before it
expires, and logs in again if the controller rejects it.

| System | Where |
|---|---|
| With a keychain | The keychain, next to the secret |
| Linux without a keychain | An owner-only file in the runtime directory (`$XDG_RUNTIME_DIR/gomada/`), which lives in memory and is wiped at logout |

With a valid token the secret is not needed, so on a server you type the
passphrase about once every two hours, not on every command. Turn the cache
off with `--no-token-cache`, or with `no_token_cache: true` in the config.
`gomada host remove` deletes the secret and the token.

### Environment variables

| Variable | Use |
|---|---|
| `GOMADA_CLIENT_SECRET` | The client secret; wins over any stored secret |
| `GOMADA_PASSPHRASE` | The passphrase of an encrypted secret file, for use without a terminal |
| `GOMADA_CONFIG` | The config file to use |
| `NO_COLOR` | Turns off color in the interactive view |

## Requirements and limits

- An Omada controller that offers the Open API (software or hardware
  controller with **Settings > Platform Integration > Open API**).
- Cloud-based controllers are not supported yet.
- In the interactive view a blocked client disappears from the list, because
  only connected clients are listed. Unblock it with `gomada client unblock`.

## Development

```sh
make check    # lint, tests with the race detector, coverage floor, cross-compile
make golden   # rewrite expected output after an intended change
```

No test needs a real controller: `internal/omada/omadatest` is a fake that
speaks the same HTTP API. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
layout and conventions, and [docs/adr](docs/adr) for why things are the way
they are.

## License

[MIT](LICENSE)
