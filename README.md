# rdcli — headless RustDesk client CLI

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Release](https://img.shields.io/github/v/release/Keviince/rustdesk-cli)](https://github.com/Keviince/rustdesk-cli/releases)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/Keviince/rustdesk-cli/release.yml?label=release%20builds)](https://github.com/Keviince/rustdesk-cli/actions)

This fork of [4nkitd/rustdesk-cli](https://github.com/4nkitd/rustdesk-cli) fixes self-hosted relay negotiation, adds explicit relay configuration and headless API account login, and restores the CLI build entry point. It retains the upstream AGPL-3.0 license.

A pure-Go CLI that speaks the RustDesk wire protocol directly — no GUI, no official binary required. Connect to any RustDesk machine by ID, name, or tag and do real work: remote file transfer, TCP tunnels, and remote shells. Output is designed for both humans and AI agents (stable aligned tables + strict JSON).

> ⚠️ **Not the botnet.** In early 2026 a Go-based botnet ("Go Client", ~2M IPs) was reported scanning and brute-forcing RustDesk endpoints (rustdesk/rustdesk#14167). rdcli is unrelated to it: this is auditable open-source software that **only ever connects to machine refs you explicitly provide** — it has no scanning, no ID enumeration, and no brute-force mode. Never accept connections from clients you don't recognize, and use strong peer passwords.

![rdcli demo](docs/demo.svg)

```
rdcli -c home-pc ls C:/Users/jane
rdcli -c home-pc cp ./backup.tar.gz C:/Users/jane/Desktop/
rdcli -c home-pc sh "ipconfig"
rdcli -c home-pc tunnel -L 3389:localhost:3389
```

## Why

RustDesk's public server now requires login, and the desktop GUI is the only official client. `rdcli` speaks the protocol itself: it reuses your existing RustDesk login (including the desktop app's remembered credentials), punches through NAT via hbbs, relays via hbbr, and authenticates to the controlled peer — all from a terminal.

## Use cases

- **Scripting & automation** — run remote commands, copy files, and set up tunnels from cron, CI, or deploy scripts; exit codes and JSON output make it pipe-friendly.
- **AI agents** — `--json` everywhere and no interactive prompts means a model can drive it: list devices, inspect a machine, pull logs.
- **Headless boxes** — manage machines that only have RustDesk installed (no display, no GUI session) straight from your terminal.
- **Homelab / self-hosted RustDesk** — point it at your own hbbs/hbbr with `--server`/`--key`, or use the Pro address-book API for device discovery.
- **RDP/SSH jump** — `tunnel -L` turns any RustDesk peer into a bastion: `rdcli -c home-pc tunnel -L 3389:localhost:3389` then `mstsc`.

## Install

### Prebuilt binaries

Grab the right `rdcli-<os>-<arch>` asset and `checksums.txt` from [this fork's Releases](https://github.com/Keviince/rustdesk-cli/releases). Linux and macOS builds are available for amd64 and arm64. Verify the downloaded asset, then install it:

```sh
sha256sum --ignore-missing --check checksums.txt   # Linux
# macOS: awk '$2 == "rdcli-darwin-arm64"' checksums.txt | shasum -a 256 -c -
chmod +x rdcli-linux-amd64
sudo mv rdcli-linux-amd64 /usr/local/bin/rdcli
```

### From source

```sh
git clone --branch main https://github.com/Keviince/rustdesk-cli.git
cd rustdesk-cli
go build -trimpath -o rdcli ./cmd/rdcli
```

## Quick start

```sh
# 1. Pull your machines + passwords from the desktop app (optional but handy)
rdcli import-gui

# 2. Check login status (the CLI can reuse your RustDesk GUI login)
rdcli auth status

# 3. Remember a machine's password (by id, name, or tag)
rdcli connect home-pc -p "your-password"

# 4. Go
rdcli -c home-pc ls C:/Users/jane
rdcli -c home-pc cp ./file.txt C:/Users/jane/Desktop/
rdcli -c home-pc cp C:/Users/jane/Desktop/file.txt ./
rdcli -c home-pc sh "whoami"
rdcli -c home-pc tunnel -L 8080:localhost:80
```

Machine refs resolve in this order: **exact ID → name (case-insensitive substring) → tag → raw ID**.

## Authentication

The public RustDesk server requires a login token. `rdcli` gets one from:

1. `--token` flag
2. `RDC_TOKEN` env var
3. `access_token` in your `config.toml`
4. **The desktop GUI's stored login** (auto-detected from your RustDesk install)

```sh
rdcli auth status        # show login state, token source, user info
rdcli auth login --help  # account login to a self-hosted API
rdcli auth import-gui    # persist the desktop app's token into rdcli config
rdcli auth set <token>   # set a token explicitly
rdcli auth logout        # clear the stored token
```

With a token present, `rdcli` performs the full hbbs `KeyExchange` handshake (signed server key + NaCl box session) and sends the token as `PunchHoleRequest.token` — the same flow as the official client.

Saved peer passwords work too: `rdcli` understands the desktop app's encrypted `00...` password blobs (decrypts them with the machine key like the GUI does) as well as plaintext passwords.

### Headless account login

`auth login` is included starting with `v0.1.2`. It supports the `lejianwen/rustdesk-api` account-login protocol (`POST /api/login`) and compatible API servers. Use the API server URL, typically an HTTPS reverse proxy or API port `21114`, rather than the hbbs port `21116`.

```bash
read -rs -p 'API account password: ' account_password
printf '\n'
printf '%s' "$account_password" | rdcli auth login \
  --api-server "https://api.example.com" --username alice --password-stdin
unset account_password
rdcli auth status
```

The command itself is non-interactive. For automation, `RDC_API_SERVER`, `RDC_API_USERNAME`, and `RDC_API_PASSWORD` can supply the login parameters. `--password`/`-p` on `auth login` means the **account password**; `RDC_PASSWORD` remains the **controlled peer password** and is never used for account login. `--password-stdin` takes precedence over the environment and cannot be combined with `--password`. `--timeout` limits the API request.

On success, the command saves `access_token`, `login_server`, `login_username`, and a stable `login_uuid`. It does not save the supplied account password or alter `api_server`/address-book configuration. Subsequent logins reuse the saved server, username, and UUID; existing `api_server` and `api_username` are also accepted as initial defaults. Flags override `RDC_API_*`, which override saved settings. Login failures preserve the existing token and configuration.

Server URLs ending in `/api` or `/api/login` are accepted, including a reverse-proxy path prefix. HTTPS verifies the server certificate; HTTP is allowed automatically for loopback addresses. An existing HTTP deployment on a trusted network can be selected explicitly with `--allow-http` on each login. Redirects are rejected so account passwords are not forwarded to another endpoint.

`auth login --json` reports success without exposing the password or token. `auth status --json` masks the `token` field and reports the effective source (`flag`, `env`, `config`, or `gui`). A pre-existing `--token`/`RDC_TOKEN` still overrides the newly saved login token; clear that override to use the saved login. `auth logout` removes the saved token locally. Password-disabled/SSO-only deployments and additional-verification responses require an existing token via `auth set` or `auth import-gui`.

Protocol references: lejianwen/rustdesk-api [`LoginForm`](https://github.com/lejianwen/rustdesk-api/blob/c5687e150615bfb00d9d105f0883a2802750a8ad/http/request/api/user.go) and [`Login`](https://github.com/lejianwen/rustdesk-api/blob/c5687e150615bfb00d9d105f0883a2802750a8ad/http/controller/api/login.go). Compatibility is tested with mock API servers; no real self-hosted server credentials were used.

## Commands

### `rdcli ls <ref> [remote_path]`

List a remote directory (default: current dir).

```sh
rdcli -c home-pc ls
rdcli -c home-pc ls C:/Users/jane --json
```

### `rdcli cp <ref> <src> <dst> [-r]`

Copy files either direction. Direction is auto-detected: a destination starting with `/` or a drive letter (`C:`) is remote.

```sh
rdcli -c home-pc cp ./backup.zip C:/Users/jane/Desktop/          # upload
rdcli -c home-pc cp C:/Users/jane/Desktop/backup.zip ./          # download
rdcli -c home-pc cp -r ./project C:/Users/jane/project           # recursive
rdcli -c home-pc cp -d up ./f.txt C:/Users/jane/f.txt            # force direction: up|down
```

Progress bar goes to stderr (stdout stays clean for pipes and JSON).

### `rdcli sh <ref> [command]`

Interactive PTY without a command; one-shot execution with one.

```sh
rdcli -c home-pc sh
rdcli -c home-pc sh "ipconfig /all"
```

### `rdcli tunnel <ref> -L <lport>:<rhost>:<rport> [--once]`

TCP port forward through the peer.

```sh
rdcli -c home-pc tunnel -L 3389:localhost:3389      # RDP through the peer
rdcli -c home-pc tunnel --once -L 0:localhost:22    # single stream, pipes stdin/stdout
```

### `rdcli devices` / `rdcli tag`

Address book and local tags.

```sh
rdcli devices                     # table: ID NAME TAGS ONLINE PLATFORM LAST_SEEN
rdcli devices --online --tag prod
rdcli devices search home --json
rdcli tag add 1234567890 prod
rdcli tag rm 1234567890 prod
rdcli tag ls
```

### `rdcli connect` / `rdcli disconnect`

Save/forget peer passwords in config (supports id, name, or tag refs).

```sh
rdcli connect home-pc -p "your-password"
rdcli connect 1234567890          # prompts-free: uses RDC_PASSWORD / config only
rdcli disconnect home-pc
```

### `rdcli ping <ref>`

Online check via the rendezvous server (exit 0 = online, 1 = offline).

## Common flags

| Flag | Env | Description |
|---|---|---|
| `-c, --connect <ref>` | | Machine ref: id, name, or tag |
| `--password <pw>` | `RDC_PASSWORD` | Peer password |
| `--token <tok>` | `RDC_TOKEN` | Login token (falls back to config / GUI) |
| `--server <host:port>` | `RDC_SERVER` | Rendezvous server (default: rs-ny.rustdesk.com:21116) |
| `--relay-server <host:port>` | `RDC_RELAY` | Relay server override; separate from `--relay` |
| `--key <key>` | `RDC_KEY` | Licence key for self-hosted servers |
| `--relay` | | Force relay, skip hole punching |
| `--yes` | | Accept insecure direct-IP connections |
| `--json` | | Machine-readable JSON output |
| `--timeout <duration>` | | Connect timeout (default 10s) |

## JSON output

All output is a single JSON object on stdout; errors go to stderr; no progress/colors pollute stdout.

```json
{"version":1,"devices":[{"id":"1234567890","name":"home-pc","tags":["windows"],"online":true,"platform":"Windows","last_seen":"","source":"local"}]}
```

```json
{"version":1,"path":"C:/Users/jane","entries":[{"name":"Desktop","type":"dir","size":0,"modified_time":1755317100,"is_hidden":false}]}
```

## Config

`~/.config/rdcli/config.toml` (override with `$RDC_CONFIG`):

```toml
server = ""               # empty = public rustdesk.com servers
relay_server = ""         # empty = announced relay, then rendezvous host:port+1
key = ""                  # self-hosted licence key
access_token = ""         # public-server login token (see `rdcli auth`)
login_server = ""         # saved API login endpoint (auth login)
login_username = ""       # saved API account name
login_uuid = ""           # generated device identity reused across logins

api_server = ""           # self-hosted Pro API base URL (address book)
api_username = ""
api_password = ""

[peers."1234567890"]
password = "00AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
name = "home-pc"

[tags]
"1234567890" = ["windows", "prod"]
```

`rdcli import-gui` populates peers from your desktop app automatically. The config file is written with `0600` permissions.

### Self-hosted relay configuration

`server`, `relay_server`, and `key` use this order: nonempty command-line flag → environment variable → config file. An explicit relay address overrides the server's announcement. Without an override, the announced relay is used; if absent, the rendezvous host and its port plus one are used (normally `21116` → `21117`). A relay address without a port uses `21117`. IPv6 addresses with ports use brackets, such as `[2001:db8::1]:21117`.

```sh
export RDC_SERVER="rd.example.com:21116"
export RDC_RELAY="relay.example.com:21117"  # independent relay or custom port
export RDC_KEY="your-server-public-key"
export RDC_TOKEN="your-login-token"        # for servers requiring login
export RDC_PASSWORD="your-peer-password"
rdcli --relay -c 123456789 sh "whoami"
```

`--relay` forces a relay session. `--relay-server` and `RDC_RELAY` select its address without forcing relay use when a direct connection succeeds. Empty-host addresses such as `:21117`, URL schemes, and example placeholders are rejected. For a custom rendezvous port that does not map to relay port plus one, specify the relay explicitly. `RDC_DEBUG=1` prints connection diagnostics to stderr.

Token import and `auth set` remain available alongside account login. The relay flows are covered by local protocol mocks; compatibility with your own server and Windows peer still needs a real-environment check.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Runtime / auth / network error |
| 2 | Usage error |

## Limitations

Honest list — read before you rely on it:

- **No screen sharing, clipboard sync, or audio.** This is a headless client for file/terminal/tunnel work, not a full remote-desktop replacement.
- **Direct hole punching uses TCP only**, so some symmetric-NAT peers require a reachable, correctly configured relay.
- **Terminal requires peer ≥ 1.4.1** (older peers reject terminal sessions).
- **Public server needs a login token** — same as the official client since RustDesk enabled mandatory login.
- **Not affiliated with RustDesk.** An independent, clean-room-ish port of the wire protocol (see License).

## FAQ

**Is this an official RustDesk tool?**
No. It's an independent CLI that interoperates with the RustDesk ecosystem (public servers, self-hosted hbbs/hbbr, and the official desktop client).

**Is rdcli the "Go Client" botnet?**
No. That's a separate ~2M-IP credential attack reported in [rustdesk/rustdesk#14167](https://github.com/rustdesk/rustdesk/discussions/14167). rdcli is fully auditable AGPL-3.0 source, connects only to IDs/names/tags you explicitly pass, and has no scanning or brute-force functionality. Contributions and security reviews are welcome so the code stays trustworthy.

**Can I use it with my self-hosted RustDesk server?**
Yes — pass `--server <host:port>` and `--key <licence-key>`, or set them in config. Self-hosted Pro address books work via `api_server`/`api_username`/`api_password`.

**Do I need the RustDesk GUI installed?**
No. The GUI is optional — `import-gui` is a convenience to reuse an existing login and saved passwords. Everything works standalone with `--token`/`RDC_TOKEN`.

**Is it safe to store passwords in config?**
The config file is written with `0600` permissions (owner-only). Peer passwords are stored the same way the desktop app does; encrypted `00...` blobs are decrypted only in memory for the handshake.

**Why AGPL-3.0?**
The wire protocol details were ported from the AGPL-licensed RustDesk sources, so rdcli is AGPL-3.0 too. That's the correct and honest license for this project.

## Protocol notes

- **Framing**: variable-length little-endian header (1–4 bytes), bottom 2 bits = header length, remaining bits = payload length (ported from `bytes_codec.rs`).
- **Crypto**: NaCl `box` for key exchange, `secretbox` for streams with sequential nonces; Ed25519 server-signed identity blobs (signature-first layout, like sodiumoxide `sign::sign`).
- **Rendezvous**: `PunchHoleRequest` over TCP (UDP NAT test not implemented — direct punch may be limited; relay always works), token-authenticated via hbbs `KeyExchange`.
- **Login**: password hashed `sha256(sha256(pw+salt)+challenge)`; supports the GUI's encrypted `00`-prefixed password blobs.

## Roadmap

- UDP NAT traversal for reliable direct connections
- Windows release binaries in CI (formula/build matrix already prepared)
- Clipboard and session-aware file resume improvements
- Shell completion scripts (bash/zsh/fish)

## Development

```sh
go build ./...
go test ./...
go vet ./...
```

Release builds for linux/darwin (amd64+arm64) are automated via GitHub Actions on `v*` tags after Linux and macOS build, test, and vet checks. Linux amd64 artifacts also run a version smoke check. Each release includes SHA-256 checksums.

## License

AGPL-3.0 — this project ports wire-protocol details from the AGPL-licensed [RustDesk](https://github.com/rustdesk/rustdesk) sources.

---

Like it? [Star the repo](https://github.com/4nkitd/rustdesk-cli) and tell me what you'd use it for. Issues, PRs, and wild feature requests are welcome.
