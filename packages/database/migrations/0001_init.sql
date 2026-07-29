-- Desktop engine schema (docs/ARCHITECTURE.md §11.1).
-- All timestamps are unix seconds. The daemon is the single writer.

-- Local account cache. Secrets live in the OS keychain, never here.
CREATE TABLE users (
    id           TEXT PRIMARY KEY,
    email        TEXT NOT NULL,
    display_name TEXT,
    jwt_ref      TEXT,               -- opaque keychain handle, NOT the token
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Transport sessions (SLKT primary or direct failover).
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    transport    TEXT NOT NULL CHECK (transport IN ('slkt', 'direct')),
    server_addr  TEXT,
    state        TEXT NOT NULL,
    opened_at    INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER
);

-- One row per file transfer: the upload-level state machine.
CREATE TABLE uploads (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    cms_file_id  TEXT,
    source_path  TEXT NOT NULL,
    filename     TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    status       TEXT NOT NULL,
    priority     INTEGER NOT NULL DEFAULT 0,
    transport    TEXT,
    error        TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE INDEX idx_uploads_status ON uploads(status);

-- Durable manifest, 1:1 with uploads.
CREATE TABLE upload_manifests (
    upload_id     TEXT PRIMARY KEY REFERENCES uploads(id) ON DELETE CASCADE,
    sha256        TEXT,
    chunk_size    INTEGER NOT NULL,     -- adaptive; equals the multipart part size
    chunk_count   INTEGER NOT NULL,
    multipart_id  TEXT,
    object_key    TEXT,
    session_id    TEXT REFERENCES sessions(id),
    bytes_done    INTEGER NOT NULL DEFAULT 0,
    cur_speed_bps INTEGER NOT NULL DEFAULT 0,
    avg_speed_bps INTEGER NOT NULL DEFAULT 0,
    eta_seconds   INTEGER,
    version       INTEGER NOT NULL DEFAULT 1,
    updated_at    INTEGER NOT NULL
);

-- Per-part state. "Missing chunks" = rows in state ('pending','inflight').
CREATE TABLE chunks (
    upload_id    TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    idx          INTEGER NOT NULL,       -- 0-based part index
    offset_bytes INTEGER NOT NULL,
    length_bytes INTEGER NOT NULL,
    sha256       TEXT,
    crc32c       INTEGER,
    etag         TEXT,
    state        TEXT NOT NULL,
    retry_count  INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (upload_id, idx)
);
CREATE INDEX idx_chunks_state ON chunks(upload_id, state);

-- Worker/stream telemetry snapshots.
CREATE TABLE workers (
    id             TEXT PRIMARY KEY,
    upload_id      TEXT REFERENCES uploads(id) ON DELETE CASCADE,
    stream_id      INTEGER,
    state          TEXT NOT NULL,
    cur_chunk      INTEGER,
    throughput_bps INTEGER,
    updated_at     INTEGER NOT NULL
);

-- Time-series for the Transfer Statistics graphs.
CREATE TABLE transfer_statistics (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    upload_id   TEXT REFERENCES uploads(id) ON DELETE SET NULL,
    ts          INTEGER NOT NULL,
    bytes_delta INTEGER NOT NULL,
    speed_bps   INTEGER NOT NULL,
    rtt_ms      INTEGER,
    inflight    INTEGER,
    loss_pct    REAL
);
CREATE INDEX idx_stats_upload_ts ON transfer_statistics(upload_id, ts);

-- Key/value app + engine settings.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Ordered scheduling view (priority + aging).
CREATE TABLE queue (
    upload_id   TEXT PRIMARY KEY REFERENCES uploads(id) ON DELETE CASCADE,
    priority    INTEGER NOT NULL DEFAULT 0,
    enqueued_at INTEGER NOT NULL,
    eligible_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_queue_order ON queue(priority DESC, enqueued_at ASC);

-- Structured local logs (Logs screen + export).
CREATE TABLE logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    level       TEXT NOT NULL,
    component   TEXT NOT NULL,
    upload_id   TEXT,
    msg         TEXT NOT NULL,
    fields_json TEXT
);
CREATE INDEX idx_logs_ts ON logs(ts);

-- Append-only, hash-chained audit trail (SOC2).
CREATE TABLE events (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    actor        TEXT,
    kind         TEXT NOT NULL,
    upload_id    TEXT,
    payload_json TEXT,
    prev_hash    TEXT NOT NULL,
    hash         TEXT NOT NULL
);
