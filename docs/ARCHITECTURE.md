# Slike Uploader — Architecture

> Last verified: 2026-09-08 against commit `6f3e3e7` (branch `dev`, working tree with in-progress uncommitted changes to `apps/uploader-desktop`)

This document describes the system **as implemented**, not as originally planned. An earlier draft (v0.1, 2026-07-21) proposed a considerably larger design — gRPC-based desktop↔daemon IPC, a rich multi-frame-type SLKT protocol, a two-level fair-share scheduler, JWKS-based auth, Postgres-backed multi-instance server state. Most of that draft was not built as specified; this rewrite replaces it with what actually exists in code, and calls out the gaps explicitly in [Architecture Gaps and Unclear Areas](#architecture-gaps-and-unclear-areas).

---

## At a Glance

- **What it is:** a desktop-driven large-file upload platform for the Slike Video CMS, built to replace browser uploads. A local engine (daemon) chunks a file, uploads it as an S3-style multipart upload over one of several interchangeable transports, and verifies the result end-to-end by SHA-256.
- **Language/stack:** Go (module `code.sli.ke/go/vega`, Go 1.25) for every backend component; the desktop shell is Wails v3 (Go + React/TypeScript, its own nested Go module).
- **Major components:** `apps/uploaderd` (background engine daemon), `apps/uploader-desktop` (Wails desktop UI, thin controller), `apps/upload-server` + `apps/upload-server/cmd/upload-server-h3` (server-side multipart terminators), `apps/uploader-cli` (older single-process CLI harness), and a set of shared `packages/*` libraries.
- **Where to start reading:** `packages/uploader/engine.go` (the orchestrator), `packages/transport/auto.go` (transport selection), `packages/storage/storage.go` (the core `ObjectStore` port), `packages/database/migrations/0001_init.sql` (schema), `apps/uploaderd/main.go` and `apps/uploader-desktop/main.go` (the two real processes).
- **Notable fact for anyone extending this:** the "chunk = S3 multipart part" design from the original draft is real and enforced everywhere (`packages/manifests/plan.go`).

---

## System Overview

The product replaces slow, memory-bounded browser uploads with a native engine that streams files directly from disk, chunks them to match S3/Spaces multipart limits, and uploads chunks over whichever transport currently works — a custom UDP+TCP protocol (SLKT), HTTP/3 (QUIC), plain HTTP, or a presigned direct-to-storage path — with automatic failover between them (`packages/transport/breaker.go`, `failoverstore.go`).

Two OS processes are central *(Verified — see [Startup and Initialization](#startup-and-initialization))*:

1. **`uploaderd`** — a long-lived daemon that owns all upload state (SQLite), runs the engine, and exposes it over a local Unix-domain-socket API.
2. **`uploader-desktop`** — a Wails v3 GUI that holds no upload state itself; it is a client of `uploaderd` over that socket, and will spawn the daemon if it isn't already running.

A separate server side (`apps/upload-server`, `apps/upload-server/cmd/upload-server-h3`) terminates the various transports, drives S3-style multipart uploads against object storage, and notifies the CMS when a file is fully assembled and verified.

`apps/uploader-cli` is an older, still-present single-process harness that runs the same engine packages directly, in-process, with no daemon/IPC involved — it predates the daemon/desktop split and duplicates responsibility that `uploaderd` now owns *(Observed)*.

---

## Project Structure

```
apps/
  uploaderd/                 background engine daemon (real process #1)
  uploader-desktop/          Wails v3 GUI, thin controller (real process #2, separate Go module)
  upload-server/             HTTP(S) + SLKT multipart server (cmd/upload-server)
  upload-server/cmd/upload-server-h3/  HTTP/2 + HTTP/3(QUIC) variant of the same server, for comparison
  uploader-cli/              older single-process CLI that runs the engine directly, no IPC
packages/
  common/        domain vocabulary (UploadStatus, ChunkState), typed IDs, EventBus, sentinel errors
  logger/        slog JSON logger wrapper
  telemetry/     bandwidth/ETA EWMA sampler + in-memory metrics counters
  database/      SQLite Open (WAL) + versioned migration runner + embedded migrations/
  manifests/     chunk-size planning + SQLite-backed upload/chunk persistence (the source of truth)
  resumable/     one function: reset InFlight chunks to Pending on startup
  scheduler/     bounded-concurrency task runner (errgroup wrapper) — not a fairness scheduler
  uploader/      the engine: Engine, Manager, Supervisor, Notifier (orchestration + state transitions)
  storage/       ObjectStore port + MemStore (fake) + S3Store (minio-go, real backend)
  protocol/      SLKT binary frame codec (header, types, CRC32C)
  transport/     ObjectStore-compatible adapters: HTTP, HTTP/3, presigned-direct, SLKT, failover chooser
  auth/          Verifier port + DevVerifier (static token) + JWTVerifier (hand-rolled HS256)
  ipc/           HTTP+JSON+SSE API served over a Unix domain socket, shared by desktop UI and (indirectly) CLI
  bench/         test-only UDP-relay + comparative benchmark of SLKT vs HTTP/3, not shipped
```

`apps/uploader-desktop` is a **nested Go module** (`apps/uploader-desktop/go.mod`, module `code.sli.ke/go/vega/apps/uploader-desktop`) with its own `frontend/` React/TypeScript/Tailwind app and Wails-generated TypeScript bindings under `frontend/bindings/code.sli.ke/go/vega/...`.

---

## Startup and Initialization

**`apps/uploaderd/main.go`** (the daemon):
1. Opens the SQLite DB and runs migrations (`database.Open`, `database.Migrate`).
2. Runs `resumable.Recover` — resets any chunk left `InFlight` from a previous crash back to `Pending`.
3. Starts a `uploader.Supervisor` (long-lived queue driver).
4. Picks a transport via `transport.Choose`/`chooseTransport` (`-transport` flag: `auto|h3|slkt|http|presigned`).
5. Builds an `ipc.Server` and calls `Serve(ctx, socketPath)`, which opens a Unix domain socket (`net.Listen("unix", ...)`, `packages/ipc/server.go`), `chmod 0600`.
6. Handles `SIGINT`/`SIGTERM` for graceful shutdown.

**`apps/uploader-desktop/main.go`** (the GUI):
1. `ensureDaemon(socket)` (`daemon.go`) dials the daemon's Unix socket; if nothing answers, spawns `uploaderd` as a **detached** child process (`Setsid` on Unix, `DETACHED_PROCESS`/`CREATE_NEW_PROCESS_GROUP` on Windows) so it outlives the GUI.
2. Constructs a Wails `application.New(...)` with exactly one bound service, `UploadService` (`service.go`), which wraps an `ipc.Client` — it holds no upload state of its own.
3. Opens one window, wires drag-and-drop to `UploadService.Enqueue`, sets up a system tray (`tray.go`), and starts a goroutine that subscribes to the daemon's SSE event stream and re-emits events into the Wails frontend.
4. On macOS, a LaunchAgent plist (referenced in `apps/uploader-desktop/architecture.md`) can keep the daemon running independent of login session — *(Observed; plist ships with `SLIKE_SERVER` commented out, defaulting to `localhost:443` per that same doc)*.

**`apps/upload-server/cmd/upload-server/main.go`** (server, SLKT variant):
1. Selects a storage backend via `-backend mem|s3` (`storage.NewMemStore` or `storage.NewS3Store`).
2. Builds an `auth.Verifier` (`-jwt-secret` → `auth.JWTVerifier`, else `auth.DevVerifier`).
3. Builds/loads a TLS config (`buildTLS`, real cert via `-tls-cert/-tls-key`, else a self-signed dev cert from `devcert.go`).
4. Opens **one TCP listener and one UDP listener on the same `-addr`** (default `:443`). The TCP listener is demultiplexed by peeking the first byte of each connection (`mux.go`): `0x16` (TLS ClientHello) routes to the HTTPS API listener, anything else routes to the SLKT control listener.
5. Runs `slkt.NewServer(objects, log).Serve(...)` over the SLKT TCP + UDP, and an `http.Server` over the TLS-wrapped HTTPS listener serving `uploadserver.New(...).Handler()`.

**`apps/upload-server/cmd/upload-server-h3/main.go`** — a separate binary sharing the same `uploadserver.New(...).Handler()` route set, but serving it over plain TCP (HTTP/1.1+2, for failover) and UDP (HTTP/3 via `quic-go/http3`) on `:443`, with no SLKT and no demux. Its own doc comment says it exists "kept beside the SLKT-multiplexing upload-server for comparison."

**`apps/uploader-cli/main.go`** — no daemon/socket involved; wires `database.Open`, `manifests.NewStore`, and `uploader.New` directly, then either runs one file (default), enqueues without running (`-enqueue-only`), or drains the whole queue continuously (`-serve`, via `resumable.Recover` + `uploader.NewManager(...).RunQueue`).

---

## Components and Responsibilities

### `packages/uploader` — the engine (`Engine`, `Manager`, `Supervisor`, `Notifier`)

- **`Engine`** (`engine.go`) — owns the per-upload lifecycle:
  - `Enqueue` — hashes the whole source file up front (`hashFile`), plans chunk size/count (`manifests.PlanChunkSize`/`PlanChunks`), persists via `manifests.Store.CreateUpload`, appends a hash-chained audit event, emits `upload.created`.
  - `Run` — resumable: resets any `InFlight` chunks to `Pending`, initializes the multipart upload if needed, computes the missing-chunk set, and runs one `scheduler.Task` per missing chunk through `scheduler.Run` (a bounded-concurrency `errgroup`, not a separate worker-pool abstraction).
  - `uploadChunk` — streams the chunk's byte range via `io.SectionReader` + `io.TeeReader` (computing SHA-256 on the fly), uploads the part, marks it acked, updates progress/telemetry, emits a `progress` event.
  - `finish` — completes the multipart upload, then **re-reads the assembled object and verifies its whole-file SHA-256 before marking the upload `Completed`** — the one integrity invariant from the original design that is fully implemented and enforced.
  - `Abort` — aborts the multipart upload and marks the upload `Canceled`.
- **`Manager`** (`manager.go`) — a one-shot queue drainer: lists active uploads and runs each through `Engine.Run` via `scheduler.Run`, swallowing per-upload errors so one failure doesn't cancel the rest. Used by `apps/uploader-cli -serve`.
- **`Supervisor`** (`supervisor.go`) — a long-lived queue driver used by `apps/uploaderd`: tracks running uploads by cancel-func, `Kick()` backfills free concurrency slots from the queue, and exposes `Pause`/`Resume`/`Cancel`/`Stop`. Each finished upload's goroutine calls `Kick()` again to pull the next one.
  - *(Observed inconsistency)* `Manager` reads the queue via `manifests.Store.ListActive`, while `Supervisor` reads it via `manifests.Store.RunnableUploads` — the latter excludes `paused` uploads, the former does not. Two independent queue-read paths with different pause semantics currently coexist.
- **`Notifier`** (`notifier.go`) — `LogNotifier` (writes to the log) and `HTTPNotifier` (HMAC-signed CMS webhook POST with an `Idempotency-Key` header) both implement `AssetReady(...)`.

State is represented as plain string constants (`common.UploadStatus`, `common.ChunkState`) set ad hoc by `SetStatus`/`Mark*` calls scattered through `engine.go`/`supervisor.go` — there is **no enum-driven transition table or session-lifecycle state machine** as such (see Gaps).

### `packages/manifests` — persistence and chunk planning

- `plan.go` — `PlanChunkSize`/`PlanChunks`: real adaptive chunk-size math, clamped to S3/Spaces multipart limits (`MinPartSize = 5 MiB`, `MaxPartSize = 5 GiB`, `MaxParts = 10000`).
- `store.go` — a `Store` backed directly by `database/sql` against SQLite: `CreateUpload`, `LoadUpload`, `MissingChunks`/`AckedChunks`, `MarkInFlight`/`MarkAcked`/`MarkPending`/`ResetInFlight`, `DoneBytes`/`UpdateProgress`/`RecordStat`, `ListActive`/`RunnableUploads`, `ListSummaries`/`LoadSummary`, `SetSetting`/`GetSetting`, and `AppendEvent` (a genuine hash-chained audit log: `hash = SHA256(prev_hash ‖ payload)`, matching the original SOC2 design intent).

### `packages/database` — schema and migration runner

- `db.go` — `Open(ctx, path)` via `modernc.org/sqlite` (pure Go, no cgo), WAL mode, `busy_timeout=5000`, `foreign_keys=1`, connection pool capped at 1 (single-writer).
- `migrate.go` — forward-only runner using `//go:embed migrations/*.sql`, tracked in a `schema_migrations` table.
- `migrations/0001_init.sql` — the **only** migration; creates `users`, `sessions`, `uploads`, `upload_manifests`, `chunks`, `workers`, `transfer_statistics`, `settings`, `queue`, `logs`, `events`, matching the original design's schema closely (see [Data and Storage](#data-and-storage)).

### `packages/scheduler` — bounded concurrency, not fairness

`pool.go` (28 lines) is the entire package — its own doc comment calls it "deliberately tiny for v1: a fixed-limit worker pool." It wraps `golang.org/x/sync/errgroup` with `SetLimit`. There is no priority queue, no weighted-fair-queuing/aging, no bandwidth governor, no BDP-driven adaptive parallelism, and no two-level (upload-level + chunk-level) distinction. The `queue` table's `eligible_at` backoff column is written once at insert (always 0) and never read again *(Gap)*.

### `packages/resumable` — crash recovery

One function, `Recover(ctx, store)`: lists active uploads, resets any `InFlight` chunk to `Pending`, sets the upload back to `Ready`. Called at startup by both `apps/uploaderd` and `apps/uploader-cli`. `Engine.Run` also does the same reset per-upload inline (`engine.go`), so the logic exists in two places.

### `packages/storage` — the `ObjectStore` port

```go
type ObjectStore interface {
    InitMultipart(ctx context.Context, key string) (uploadID string, err error)
    UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (Part, error)
    CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
    AbortMultipart(ctx context.Context, key, uploadID string) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
}
```
Plus an optional `PartPresigner` capability interface. Two concrete implementations: `MemStore` (in-memory fake, used for dev/tests and as the default backend) and `S3Store` (`s3store.go`, real `minio-go/v7` **Core API** usage — `NewMultipartUpload`, `PutObjectPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `Presign`, `GetObject` — chosen specifically so one protocol chunk maps 1:1 to one multipart part). Every transport adapter in `packages/transport` and `packages/transport/slkt` implements this same interface, confirming the "one interface for every transport" design does hold in code.

### `packages/transport` — the multiple upload paths

All of these are real, compiled, and reachable — confirmed by both `apps/uploaderd` and `apps/uploader-cli` calling `transport.Choose(...)` (`auto.go`):

- **`HTTPStore`** (`httpstore.go`) — proxies multipart operations to `apps/upload-server`'s JSON API over HTTP(S).
- **`NewHTTP3Store`** (`http3.go`) — the same `HTTPStore`, wired to a `quic-go/http3.Transport` for HTTP/3.
- **`PresignedStore`** (`presigned.go`) — direct-to-storage path: control operations (init/complete/abort) go through `HTTPStore`, but `UploadPart` fetches a presigned URL from the server and `PUT`s bytes straight to object storage, bypassing the server for the data itself.
- **`slkt.Client`/`slkt.Server`** (`packages/transport/slkt/`) — see below.
- **`Chooser`/`FailoverStore`** (`breaker.go`, `failoverstore.go`) — a real circuit-breaker (closed/open/half-open, per-candidate consecutive-failure tracking, periodic re-probing) that races/falls back between the above candidates. `auto.go`'s `-transport auto` mode drives all candidates through this chooser.

**`packages/transport/slkt`** — the custom protocol, hybrid UDP (bulk data) + TCP (control/NAK):
- `Client` implements `ObjectStore` with a NAK/retransmit loop (up to 16 rounds) and a token-bucket pacer that is a **fixed 64 MiB/s placeholder** — its own comment notes BBR-style adaptive pacing is not yet implemented.
- `Server` reassembles parts via a per-packet bitset, verifies CRC32C, and reports selective NAKs.
- **Buffers full parts in memory** on both sides (`buf: make([]byte, size)`) — there is no disk-backed reassembly, so very large parts are memory-bounded in practice, contrary to the "TB-scale, bounded memory" claim in the original design.
- Trusts whatever multipart ID it is given; enforces no ownership check of its own (ownership is only checked by `apps/upload-server`'s HTTP control plane).

### `packages/protocol` — the wire frame codec

`frame.go` implements a genuine 26-byte big-endian frame header (Magic + Version + Type + Flags + StreamID + Sequence + PayloadLen + HeaderCRC32C), with `Encode`/`ReadFrame`, magic/CRC validation, and an 8 MiB payload cap. It defines ~20 frame type constants (`TypeHello`, `TypeAuth`, `TypeSessionOpen`, `TypeFlowUpdate`, `TypeDedupQuery`, etc.).

*(Gap)* Of these ~20 types, `packages/transport/slkt` only ever constructs one — `TypeChunkData`. All session/auth/flow-control/dedup/completion semantics are instead carried by a separate, ad hoc JSON control-message struct sent length-prefixed over TCP (`slkt/wire.go`), which does not use the `protocol.Type*` constants at all. The frame codec is real and tested, but most of the frame types it defines are currently unused/dead.

### `packages/auth` — token verification

`Verifier` interface: `Verify(ctx, token) (subject string, err error)`. Two implementations: `DevVerifier` (constant-time compare against one static token, dev/test only) and `JWTVerifier` (`jwt.go`) — a hand-rolled, stdlib-only HS256 verifier: 3-segment split, HMAC-SHA256 signature check, `alg == "HS256"` enforcement, `exp`/`nbf` checks, optional `iss`/`aud` checks, requires non-empty `sub`.

*(Gap)* No JWKS-based RS256 verification exists. Both the package's own doc comment and `jwt.go` state this is intentionally deferred — the production verifier for CMS-issued JWTs is future work, to plug in behind the same `Verifier` interface.

### `packages/ipc` — desktop/CLI ↔ daemon protocol

Plain **HTTP+JSON over a Unix domain socket**, with **Server-Sent Events** for the live stream — explicitly stated in the package's own doc comment. This is **not gRPC**, contradicting the original design's stated choice.

Routes (`server.go`): `POST /v1/uploads` (enqueue), `GET /v1/uploads` (list), `GET /v1/uploads/{id}` (get), `POST /v1/uploads/{id}/pause|resume|cancel`, `GET /v1/events` (SSE stream), `GET /v1/metrics`. Every request (including the SSE stream) requires a constant-time-compared bearer token. The token is generated once and stored **as a plain file** at `0600` under the daemon's state directory — **not** in the OS keychain/DPAPI/Secret Service as the original design specified *(Gap)*.

### `packages/bench` — not shipped

A test-only UDP-relay link emulator (configurable loss/latency) plus a comparative benchmark (`TestTransportComparison`) pitting `slkt` against HTTP/3 through the relay. Its own doc comment states nothing in the shipping binaries imports it.

### `apps/uploader-desktop` — the GUI

Wails v3 app, its own nested Go module. `main.go` binds exactly one Wails service, `UploadService` (`service.go`), which holds only an `*ipc.Client` — no upload state lives in the GUI process. Its six methods (`List`, `Enqueue`, `Pause`, `Resume`, `Cancel`, `Metrics`) are the entire Go→JS API surface, confirmed by the generated bindings.

Frontend (`frontend/src/`): `App.tsx` is the single stateful component — polls `List()`/`Metrics()` every 1.5s and also subscribes to an `upload.event` push (which only triggers a re-list, never applied as an incremental delta), with a plain `useState`/`useMemo` model (no Redux/Zustand/routing library). `layout.tsx` provides `Sidebar`/`TopBar`; `views.tsx` implements five screens (`QueueView`, `IngestView`, `LibraryView`, `ActivityView`, `SettingsView`); `ui.tsx` holds shared presentational components; `icons.tsx` is inline SVG; `format.ts` holds local types mirroring `packages/ipc`'s wire shapes plus formatting helpers.

A more detailed, independently evidence-verified doc for this app exists at `apps/uploader-desktop/architecture.md` (untracked, dated 2026-09-08) and is a good source for anyone extending the desktop app specifically; it additionally notes: no user-facing error surface for failed pause/resume/cancel (only `console.error`), a duplicated/drifting terminal-status list between `tray.go` and `format.ts`, and no CMS login/auth flow found anywhere in this app.

### `apps/upload-server` — server-side terminator(s)

`Server.Handler()` (`apps/upload-server/server.go`) registers, behind bearer-token auth (except the health probe):
- `POST /v1/multipart` (init)
- `PUT /v1/multipart/{id}/parts/{n}` (upload part)
- `POST /v1/multipart/{id}/complete`
- `DELETE /v1/multipart/{id}` (abort)
- `GET /v1/object` (fetch)
- `POST /v1/multipart/{id}/parts/{n}/presign` (only registered when the storage backend implements `PartPresigner`, i.e. the S3 backend)
- `GET /v1/ping` (unauthenticated reachability probe)

Ownership of each multipart ID is tracked **in-memory** (`OwnerStore`/`memOwners`) — the file's own doc comment states this is "deliberately minimal for v1" and that production would move it to a shared store (e.g. Postgres); that shared store does not exist in code *(Gap)*. There is exactly one server process type per binary — no separate control-plane/data-plane process split; the two roles are logically separated within one process by the port-sharing/demux scheme in `apps/upload-server/cmd/upload-server/mux.go`.

### `apps/uploader-cli` — legacy single-process harness

Runs the engine in-process with no `packages/ipc` involvement at all; its own doc comment describes it as "the headless controller for the engine until the Wails desktop UI is built." It now coexists with, rather than being superseded by, the daemon/desktop split *(Observed architectural inconsistency — flagged in Gaps)*.

---

## System, Request, and Data Flows

### Enqueue → upload → verify (via the daemon, any transport)

1. **Desktop UI** calls `UploadService.Enqueue(path)` → **`ipc.Client`** → `POST /v1/uploads` on the daemon's Unix socket.
2. **`uploaderd`**'s `ipc` backend calls **`Engine.Enqueue`**: streams the file once to compute its whole-file SHA-256, plans chunk size/count against S3 multipart limits, persists an `uploads`+`upload_manifests`+`chunks`+`queue` row set in one transaction, appends a hash-chained `events` row, emits `upload.created` on the `EventBus`.
3. **`Supervisor.Kick`** picks the upload up (via `RunnableUploads`) and calls **`Engine.Run`**, which resets any stale `InFlight` chunks, initializes the multipart upload (through whichever `ObjectStore` the active `transport.Chooser` currently favors), and computes the missing-chunk set.
4. One `scheduler.Task` per missing chunk runs through `scheduler.Run` (bounded concurrency): each reads its byte range via `io.SectionReader`, tees through SHA-256, calls `ObjectStore.UploadPart`, marks the chunk `Acked`, updates progress/telemetry, emits a `progress` event.
5. When all chunks are acked, **`Engine.finish`** calls `CompleteMultipart`, then re-reads the assembled object and **verifies the whole-file SHA-256** before marking the upload `Completed`. A mismatch leaves it in a non-`Completed` state rather than silently finishing.
6. `Notifier.AssetReady` (an HMAC-signed webhook with an idempotency key, or a log line in dev) tells the CMS the asset is ready.
7. The desktop UI never sees any of this directly — it polls `List()`/`Metrics()` on an interval and also re-lists on receiving an SSE `upload.event`.

### Transport selection and failover

`transport.Choose` builds whichever concrete `ObjectStore` the `-transport` flag names (`h3`, `slkt`, `http`, `presigned`), or in `auto` mode builds all of them behind a `Chooser`/`FailoverStore` that tracks per-candidate health (closed/open/half-open circuit-breaker state) and re-probes periodically. Because every candidate implements the same `ObjectStore` interface, `Engine` never branches on which transport is active — the same `InitMultipart`/`UploadPart`/`CompleteMultipart` calls work regardless of path *(Verified — this is the one core design idea from the original draft that holds fully in code)*.

### Crash recovery

On daemon (or CLI `-serve`) startup, `resumable.Recover` resets every chunk still `InFlight` back to `Pending` and its upload back to `Ready`; `Engine.Run` performs the same reset again per-upload when it is invoked. Because chunk state is persisted to SQLite (WAL mode) after every transition, no upload restarts from zero after a crash — only in-flight chunks (not yet acked) are re-sent.

---

## External Integrations

- **Object storage (DigitalOcean Spaces / any S3-compatible store)** — via `packages/storage.S3Store`, using `minio-go/v7`'s Core API for multipart operations, and via presigned URLs for the direct-upload transport path. Failure behavior: errors from `minio-go` calls propagate up through `Engine`, which leaves the affected chunk `Pending` for retry rather than the upload `Completed`.
- **Video CMS** — via `packages/uploader.HTTPNotifier`, an HMAC-signed webhook POST with an `Idempotency-Key` header, sent once a file is assembled and verified. *(Unclear)* No CMS login/authentication flow was found in `apps/uploader-desktop`, and no JWKS integration exists yet in `packages/auth` — how the daemon/desktop currently obtain and refresh a CMS-issued token is not verifiable from this repo alone.

---

## Data and Storage

**SQLite** (`packages/database`, `modernc.org/sqlite`, pure Go, WAL mode, single-writer, `packages/database/migrations/0001_init.sql`) is the only datastore for engine state, on the daemon side:

| Table | Purpose |
|---|---|
| `users` | local account cache (no secrets) |
| `sessions` | transport session bookkeeping |
| `uploads` | one row per file transfer — the upload-level status |
| `upload_manifests` | manifest fields: sha256, chunk size/count, multipart id, object key, speed/ETA |
| `chunks` | per-part state: offset, length, sha256/crc32c, etag, state, retry count |
| `workers` | worker/stream telemetry snapshots |
| `transfer_statistics` | time-series samples for graphs/diagnostics |
| `settings` | key/value app+engine settings |
| `queue` | priority/enqueued-at ordering (only `priority` is actually read; `eligible_at` backoff scheduling is written but never consumed) |
| `logs` | structured local logs |
| `events` | append-only, hash-chained audit trail (`prev_hash`/`hash` via SHA-256) |

This schema is close to a verbatim match of the original design (§11.1 of the prior draft) — the one section of that draft that was carried into code essentially unchanged.

**Server-side state** — `apps/upload-server`'s multipart-ID-to-owner mapping is in-memory only (`OwnerStore`), not the shared Postgres store the original design called for; a server restart loses this ownership map (though the underlying storage-side multipart upload itself survives, since it lives in object storage, not in the server process) *(Gap)*.

**Object storage** — final assets and in-progress multipart parts live in the configured `ObjectStore` (Spaces/S3/MinIO in production, an in-memory `MemStore` in dev/tests).

---

## Security

- **Transport encryption:** TLS on the HTTP(S) control plane (self-signed dev cert or a supplied cert/key); SLKT's own control channel is a distinct, non-TLS JSON-over-TCP channel (see [Gaps](#architecture-gaps-and-unclear-areas)).
- **AuthN (server):** `packages/auth.Verifier` — either a static dev token (`DevVerifier`) or a hand-rolled HS256 JWT check (`JWTVerifier`), enforced on every `apps/upload-server` route except `/v1/ping`.
- **AuthN (local IPC):** every `packages/ipc` request, including the SSE stream, requires a constant-time-compared bearer token generated once and stored as a `0600` plain file under the daemon's state directory.
- **AuthZ / ownership:** `apps/upload-server` tracks per-multipart-ID ownership in memory (`OwnerStore`) and checks it on part/complete/abort requests.
- **Integrity:** per-chunk SHA-256 computed on the fly during upload; whole-file SHA-256 re-verified after `CompleteMultipart` before an upload is ever marked `Completed`.
- **Audit trail:** `packages/manifests.Store.AppendEvent` writes a genuine hash-chained (`SHA256(prev_hash ‖ payload)`) `events` log.

**Not implemented** *(Gap, all explicitly noted in the code's own comments)*: JWKS/RS256 verification for CMS-issued tokens; OS-keychain/DPAPI/Secret-Service storage for the local IPC token (currently a plain file); a shared/persistent ownership store on the server (currently in-memory, single-instance only).

---

## Background and Asynchronous Processing

- **`Supervisor`** (daemon) — long-lived, tracks running uploads by cancel-func, backfills free concurrency slots from the queue on completion of each upload, and exposes `Pause`/`Resume`/`Cancel`/`Stop`.
- **`Manager`** (CLI `-serve` mode) — a one-shot queue drainer, a separate implementation with different pause semantics from `Supervisor` (see [Components](#components-and-responsibilities)).
- **`scheduler.Run`** — the actual concurrency primitive underneath both: an `errgroup` with `SetLimit`, used both for "chunks within one upload" and "uploads within one drain pass." No fairness, priority weighting, or backoff-with-jitter exists at this layer; retry-on-failure is handled instead by `manifests.Store.MarkPending` being called again from `Engine`.
- **Bandwidth/ETA sampling** — `packages/telemetry.Sampler`, a pure EWMA implementation fed per-chunk by `Engine`, exposed to the UI via `Metrics()`/`transfer_statistics`.

---

## Configuration

- **Daemon (`uploaderd`):** SQLite DB path, Unix socket path (`packages/ipc.DefaultSocketPath`), `-transport` selection (`auto|h3|slkt|http|presigned`).
- **Server (`upload-server` / `upload-server-h3`):** `-addr` (default `:443`), `-backend mem|s3` (storage backend), `-jwt-secret` (enables `JWTVerifier`; otherwise falls back to a dev token), `-tls-cert`/`-tls-key` (otherwise a self-signed dev cert is generated).
- **Desktop app:** a macOS LaunchAgent plist controls whether/how the daemon is kept alive independent of the GUI; `SLIKE_SERVER` in that plist currently ships commented out, defaulting the app to `localhost:443` per `apps/uploader-desktop/architecture.md`.

No secret values are reproduced here; none were found hardcoded in the repository during this review.

---

## Build, Runtime, and Deployment

- **Module layout:** one root Go module (`code.sli.ke/go/vega`) plus one nested module for the desktop app (`apps/uploader-desktop`, using a `replace` directive back to the monorepo root).
- **CI:** no `.github`, GitLab CI, CircleCI, or Azure Pipelines configuration exists anywhere in the repository *(Gap)*.
- **Containers:** `apps/uploader-desktop/build/docker/Dockerfile.server` and `Dockerfile.cross` build the **desktop app in "server mode"** (static Go binary + bundled frontend assets, `EXPOSE 8080`) — these are not related to `apps/upload-server`. **No Dockerfile or deploy config exists for `apps/upload-server`/`upload-server-h3` at all** *(Gap)*.
- **Desktop packaging:** Taskfile-driven builds under `apps/uploader-desktop/build/{darwin,windows,linux,android,ios}` for native installers (Wails tooling), plus icon/branding assets — some of which (`build/config.yml`) still carry Wails-scaffold placeholder branding per `apps/uploader-desktop/architecture.md`.
- **Prebuilt binaries** `uploaderd` and `upload-server-h3` exist at the repo root as build artifacts (not committed source, likely local build output — `.gitignore` should be checked before assuming these belong in version control).

---

## Design Decisions and Constraints

- **Chunk ≡ S3 multipart part**, clamped to `5 MiB ≤ part ≤ 5 GiB`, `≤ 10,000 parts` — implemented exactly as originally designed in `packages/manifests/plan.go`, and it is what lets every transport share one `ObjectStore` interface and one assembly path.
- **Two real OS processes** for the desktop product (daemon + GUI), communicating over a Unix domain socket with a per-user file token — the survives-UI-crash/close requirement from the original brief is genuinely met, just over HTTP+JSON+SSE rather than the gRPC originally specified.
- **Multiple interchangeable transports behind one interface**, selected by a circuit-breaker/failover chooser — implemented and wired into both real entry points (`uploaderd`, `uploader-cli`), not aspirational.
- **Verified-completion invariant** (whole-file SHA-256 check gates the `Completed` state) is enforced in `Engine.finish`, matching the original design's non-negotiable stated in its draft.
- **Two independent queue-driving implementations** (`Manager` for the CLI, `Supervisor` for the daemon) with subtly different pause semantics exist side by side rather than one shared driver — an implementation choice, not obviously an oversight, but worth resolving deliberately if `apps/uploader-cli` is meant to be retired.

---

## Architecture Gaps and Unclear Areas

| Area | Observed state | Gap or uncertainty | Impact |
|---|---|---|---|
| Local IPC transport | `packages/ipc` is HTTP+JSON+SSE over a Unix socket | Original design specified gRPC | Cosmetic vs. the old doc, but anyone building a new client should target the real HTTP+JSON API, not gRPC |
| SLKT frame protocol | `packages/protocol` defines ~20 frame types; `slkt` only ever sends `TypeChunkData` | Session/auth/flow-control/dedup semantics live in an ad hoc JSON control message instead of frame types | The documented wire format is largely dead code; anyone implementing an interoperable SLKT client must read `slkt/wire.go`'s JSON `ctrlMsg`, not `protocol/frame.go`'s type table |
| SLKT congestion control | Fixed 64 MiB/s token-bucket pacer, explicitly a placeholder in code | No BBR-style adaptive pacing | Throughput on lossy/high-BDP links won't adapt as the original design intended |
| SLKT part reassembly | Full part buffered in memory on both client and server | No disk-backed reassembly | Very large parts (multi-GB) are memory-bounded per in-flight part, contrary to the "TB-scale, bounded memory" design goal |
| Scheduler | `packages/scheduler` is a 28-line bounded-concurrency wrapper | No priority/fairness/aging/BDP-adaptive parallelism; `queue.eligible_at` backoff column is dead | Large and small uploads compete for the same fixed worker slots with no fairness guarantee |
| Auth | Hand-rolled HS256 `JWTVerifier` only | No JWKS/RS256 verification for real CMS-issued tokens; not clear how the desktop app currently obtains a CMS token at all | Production auth integration with the CMS is unverified/incomplete from this repo alone |
| Local token storage | Per-user IPC token stored as a `0600` plain file | Original design specified OS keychain/DPAPI/Secret Service | Token is protected only by filesystem permissions, not OS credential storage |
| Server ownership state | `apps/upload-server`'s multipart ownership map is in-memory (`OwnerStore`) | No shared/persistent store (e.g. Postgres) | Server cannot run more than one instance and cannot survive a restart without losing ownership tracking (the underlying storage upload itself is unaffected) |
| Queue drivers | `Manager` (CLI) and `Supervisor` (daemon) both drive the same `uploads`/`queue` tables independently | Different pause semantics (`ListActive` vs `RunnableUploads`); not clear if this is intentional | Running the CLI's `-serve` mode and the daemon against the same DB concurrently could produce inconsistent queue behavior |
| `apps/uploader-cli` | Still present, runs the engine in-process with no daemon/IPC | Its own doc comment frames it as a predecessor to the daemon/desktop split, but it hasn't been removed | Two independently-maintained ways to drive the same engine exist in the codebase today |
| CI / deployment for the server | No CI config anywhere in the repo; no Dockerfile for `apps/upload-server` (only for `apps/uploader-desktop`'s unrelated "server mode") | How the upload-server binaries are actually built, tested, and deployed is not verifiable from this repo | Anyone changing the server should confirm the real deployment process out-of-band before assuming Docker/CI conventions apply |
| CMS integration | `HTTPNotifier` sends an asset-ready webhook; no CMS login flow found in the desktop app | Unclear how/where the CMS JWT the daemon would need is obtained and refreshed | Any auth-related change should first establish the real current login flow, which is not visible in this repository |
