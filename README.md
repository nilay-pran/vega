# Slike Uploader

Production-grade desktop upload platform for the Slike Video CMS. The upload
engine is the product; the UI is only a controller. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design (18 sections).

## Layout

```
apps/
  upload-server/            HTTP upload server (inbound adapter) + cmd/upload-server
  uploader-cli/             headless controller that drives the engine
packages/
  common/                   domain types, typed IDs, sentinel errors (no deps)
  logger/                   slog JSON logger
  database/                 SQLite: Open (WAL) + versioned Migrate + schema
  storage/                  ObjectStore port + MemStore (fake) + S3Store (Spaces/MinIO)
  manifests/                chunk planner + SQLite-backed upload/chunk store
  scheduler/                bounded-concurrency worker pool
  telemetry/                bandwidth/ETA sampler + metrics registry
  resumable/                startup recovery of interrupted uploads
  uploader/                 the engine + Manager (queue-driven, many uploads at once)
  protocol/                 SLKT wire frame codec (custom binary protocol)
  transport/                HTTPStore + Select (probe & pick primary/failover)
  transport/slkt/           SLKT: hybrid UDP (data) + TCP (control/NAK) transport
  auth/                     bearer-token Verifier (dev now; JWKS later)
```

The engine depends only on the `storage.ObjectStore` port, so the same code runs
against the in-memory fake (tests), the HTTP transport (server-in-path), or
direct-to-Spaces — and the binary SLKT transport slots in later without touching
it.

## Build & test

```sh
go build ./...
go vet ./...
go test ./...
go test ./packages/protocol -run x -fuzz FuzzReadFrame -fuzztime 10s   # frame parser fuzz
```

Requires Go 1.24+. The only runtime dependency is a pure-Go SQLite driver
(`modernc.org/sqlite`) — no cgo, so cross-compilation is trivial.

## Run it locally

```sh
# terminal 1: server with the in-memory object store.
# -slkt-addr also starts the hybrid UDP+TCP SLKT transport.
go run ./apps/upload-server/cmd/upload-server -addr :7080 -slkt-addr 127.0.0.1:7090 -backend mem

# terminal 2, HTTP transport (server-in-path):
go run ./apps/uploader-cli -transport http -server http://localhost:7080 -db /tmp/state.db path/to/file

# or the SLKT transport (data over UDP, acks/NAK over TCP):
go run ./apps/uploader-cli -transport slkt -slkt-addr 127.0.0.1:7090 -db /tmp/state.db path/to/file
```

The CLI persists all state in SQLite, so interrupting it (Ctrl-C, crash, reboot)
and re-running the same command resumes from the missing chunks — no restart from
zero.

### Against real DigitalOcean Spaces / MinIO

```sh
export SPACES_ENDPOINT=nyc3.digitaloceanspaces.com SPACES_REGION=nyc3 \
       SPACES_KEY=... SPACES_SECRET=... SPACES_BUCKET=... SPACES_SSL=true
go run ./apps/upload-server/cmd/upload-server -backend s3
```

## Implemented vs. planned

**Done, all tested (incl. `-race`):**
foundations & schema; adaptive chunk planning within S3 multipart limits;
resumable engine with per-chunk + whole-file SHA256 verification; in-memory and
S3 object stores; server-in-path HTTP transport; **SLKT hybrid UDP+TCP transport**
(bulk data over UDP, selective-NAK reliability + control over TCP, token-bucket
pacer, loopback loss test proving retransmission); upload server with auth +
per-upload ownership; SLKT frame codec (fuzzed); **background Manager driving the
persistent queue with bounded concurrency (40+ simultaneous uploads)**; startup
crash recovery; **live bandwidth/ETA telemetry** persisted to transfer_statistics;
in-process **event bus** for controllers; metrics registry; settings KV;
**tamper-evident hash-chained audit events (SOC2)**; **real JWT verification
(HS256, claims: exp/nbf/aud/iss/sub)**; **SLKT protocol version negotiation**;
**HMAC-signed idempotent CMS webhook notifier**; **TLS-capable server**
(`-tls-cert`/`-tls-key`); **hybrid transport chooser** (`-transport auto`: probes
the custom protocol first, fails over to HTTP when the port is blocked — the
enterprise-firewall path); CLI controller with `-serve`/`-enqueue-only`/`-transport` modes.

**Next (see ARCHITECTURE.md §18 roadmap):**
direct-to-Spaces ObjectStore (client presigned S3) as a third `Select` candidate
so failover reaches object storage directly, not just HTTP (P3); TLS on the SLKT
control channel + RS256/JWKS verification against the real CMS keys; RTT-driven
BBR-style congestion control replacing the fixed-rate pacer, disk-backed
reassembly for multi-GB parts, adaptive parallelism (P4); full crash/chaos
recovery matrix (P5); keychain credential storage + code signing (P6);
Postgres-backed multi-instance server + QUIC transport (P7); Wails desktop UI —
needs the `wails` toolchain — as a second controller over the engine (P2/GA).
