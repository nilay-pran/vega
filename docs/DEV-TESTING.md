# Slike Uploader — Dev & Testing Guide

How to build, run, and test the stack locally. See [ARCHITECTURE.md](ARCHITECTURE.md) for the design.

## The pieces

| Component | Path | Role |
|-----------|------|------|
| `upload-server` | `apps/upload-server/cmd/upload-server` | Server-in-path receiver (HTTP + optional SLKT). Backends: `mem` (dev) or `s3`. |
| `uploaderd` | `apps/uploaderd` | The engine **daemon**. Long-lived; owns the queue; serves a per-user Unix socket. The product. |
| `uploader-desktop` | `apps/uploader-desktop` | Wails v3 UI. Thin controller — talks to the daemon over the socket. Nested Go module. |
| `uploader-cli` | `apps/uploader-cli` | Dev/CI driver that runs the engine **directly** (not via the daemon socket). |

Data path: UI/CLI → daemon → transport (`slkt` primary / `http` failover) → upload-server → object store.

**Per-user state** (socket, bearer token, SQLite db) lives in `os.UserConfigDir()/vega`:
- macOS: `~/Library/Application Support/vega/`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/vega/`

Default ports: HTTP `:7080`, SLKT `:7090`.

## Prerequisites

```sh
go version          # 1.24+
node -v && npm -v   # Node 20+ / npm (for the desktop frontend)

# Wails v3 CLI (desktop build/dev) — installs to $(go env GOPATH)/bin
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
export PATH="$PATH:$(go env GOPATH)/bin"

# Only for building the Windows installer on a non-Windows host:
brew install makensis           # macOS
```

## 1. Unit tests

```sh
go test ./... -race              # root module: all packages
```

The desktop app is a **nested module** — build it with `.`, never `./...` (the template's
mobile targets intentionally break `./...` inside that dir):

```sh
cd apps/uploader-desktop && go build .
```

## 2. Fast end-to-end smoke test (headless)

One command: brings up server + daemon on an **isolated** socket/db (never touches your
real state), pushes a generated file through the control socket, asserts `completed`, cleans up.

```sh
scripts/smoke.sh          # 10 MiB default
scripts/smoke.sh 200      # 200 MiB
```

Use this as the quick "did I break the data path" check and in CI.

## 3. Interactive stack + desktop UI

Run the server and daemon on the **default** socket so the UI attaches unchanged:

```sh
scripts/dev-stack.sh      # upload-server (mem) + uploaderd (http). Ctrl-C stops both.
```

In a second terminal, run the UI in hot-reload dev mode:

```sh
cd apps/uploader-desktop
wails3 task dev
```

Add files in the window → they upload through the daemon. Close the window: uploads keep
running (the daemon is separate). Relaunch: it reattaches to the same daemon.

## 4. Poking the daemon by hand (curl over the socket)

The API is HTTP+JSON over the Unix socket, bearer-token guarded.

```sh
DIR="$HOME/Library/Application Support/vega"   # macOS state dir
SOCK="$DIR/uploaderd.sock"; TOK="$(cat "$DIR/token")"
CURL() { curl -fsS --unix-socket "$SOCK" -H "Authorization: Bearer $TOK" "$@"; }

CURL http://localhost/v1/uploads                                   # list
CURL -d '{"path":"/abs/path/file.mov"}' http://localhost/v1/uploads # enqueue
CURL http://localhost/v1/uploads/<id>                              # detail
CURL -X POST http://localhost/v1/uploads/<id>/pause                # pause | resume | cancel
CURL http://localhost/v1/metrics                                   # counters
CURL -N http://localhost/v1/events                                 # live SSE stream
```

No token / wrong token → `401`.

## 5. Engine-only path (no daemon), via the CLI

Useful to test the engine in isolation. Terminal A runs the server; terminal B drains a queue:

```sh
# A: receiver
go run ./apps/upload-server/cmd/upload-server -addr :7080 -backend mem

# B: enqueue then upload (serve mode recovers + drains the queue)
go run ./apps/uploader-cli -transport http -server http://localhost:7080 -enqueue-only /path/file.mov
go run ./apps/uploader-cli -transport http -server http://localhost:7080 -serve
```

### Testing the SLKT (UDP+TCP) transport

Start the server with an SLKT listener, then point the client at it:

```sh
go run ./apps/upload-server/cmd/upload-server -addr :7080 -slkt-addr :7090 -backend mem
go run ./apps/uploader-cli -transport slkt -slkt-addr localhost:7090 -serve
# or -transport auto to probe SLKT first, fail over to http
```

### Testing against real object storage (S3 / DO Spaces)

```sh
export SPACES_ENDPOINT=... SPACES_REGION=... SPACES_KEY=... SPACES_SECRET=... SPACES_BUCKET=...
go run ./apps/upload-server/cmd/upload-server -addr :7080 -backend s3
```

## 6. Building & testing the installers

Single package per OS, bundling UI + daemon; the daemon auto-starts at login.

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
cd apps/uploader-desktop

# macOS  -> bin/uploader-desktop-installer.pkg   (must build on macOS)
wails3 task darwin:installer

# Windows -> bin/uploader-desktop-<arch>-installer.exe  (native, or cross via makensis)
wails3 task windows:installer ARCH=amd64
```

**Verify a macOS pkg without installing:**

```sh
pkgutil --payload-files bin/uploader-desktop-installer.pkg | grep -E 'uploaderd|LaunchAgents'
```

**Install-test on macOS** (installs to `/Applications`, loads the LaunchAgent):

```sh
sudo installer -pkg bin/uploader-desktop-installer.pkg -target /
launchctl list | grep slike            # daemon should be running
cat /tmp/vegad.log           # daemon logs
```

After install the daemon runs at every login (`RunAtLoad`) and restarts if it dies
(`KeepAlive`); on Windows it autostarts via an `HKCU\...\Run` entry.

## 7. Reset / teardown

Wipe daemon state so the next run starts clean (also unloads the macOS LaunchAgent):

```sh
scripts/reset-state.sh
```

## Troubleshooting

- **UI says "Upload engine not running"** — the daemon isn't up or is on a different socket.
  Run `scripts/dev-stack.sh` (default socket). The `-socket` override in `smoke.sh` is deliberately
  isolated and the UI won't see it.
- **`auto` transport hangs ~2s then picks http** — expected: it probes SLKT first. Pass
  `-transport http` in dev to skip the probe.
- **Port already in use** — a previous run's server/daemon is alive: `pkill -f uploaderd; pkill -f upload-server`.
- **`makensis: command not found`** — only needed for the Windows installer; `brew install makensis`.
- **Stale socket after a crash** — `scripts/reset-state.sh`, or delete
  `…/vega/uploaderd.sock`.
