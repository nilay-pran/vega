package manifests

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"code.sli.ke/go/vega/packages/common"
)

// Store persists uploads, manifests and per-chunk state in SQLite. The daemon
// is the single writer, so no locking beyond SQLite's own is needed.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() int64 { return time.Now().Unix() }

// UpsertUser makes sure a user row exists so the uploads FK is satisfied.
func (s *Store) UpsertUser(ctx context.Context, id common.UserID, email string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET email = excluded.email, updated_at = excluded.updated_at`,
		id, email, now(), now())
	return err
}

// CreateUpload inserts the uploads, upload_manifests, queue and chunk rows in
// one transaction. The upload starts in StatusPreparing.
func (s *Store) CreateUpload(ctx context.Context, u *Upload, chunks []Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ts := now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO uploads (id, user_id, cms_file_id, source_path, filename, size_bytes, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.UserID, u.CMSFileID, u.SourcePath, u.Filename, u.Size, u.Status, ts, ts); err != nil {
		return fmt.Errorf("insert upload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO upload_manifests (upload_id, sha256, chunk_size, chunk_count, object_key, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.SHA256, u.ChunkSize, u.ChunkCount, u.ObjectKey, ts); err != nil {
		return fmt.Errorf("insert manifest: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO queue (upload_id, priority, enqueued_at) VALUES (?, 0, ?)`, u.ID, ts); err != nil {
		return fmt.Errorf("insert queue: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks (upload_id, idx, offset_bytes, length_bytes, state, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		if _, err := stmt.ExecContext(ctx, u.ID, c.Index, c.Offset, c.Length, c.State, ts); err != nil {
			return fmt.Errorf("insert chunk %d: %w", c.Index, err)
		}
	}
	return tx.Commit()
}

// LoadUpload returns the joined upload + manifest, or common.ErrNotFound.
func (s *Store) LoadUpload(ctx context.Context, id common.UploadID) (*Upload, error) {
	u := &Upload{}
	var multipart, key, sha sql.NullString
	var cmsFileID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.user_id, u.cms_file_id, u.source_path, u.filename, u.size_bytes, u.status,
		       m.object_key, m.multipart_id, m.sha256, m.chunk_size, m.chunk_count
		FROM uploads u JOIN upload_manifests m ON m.upload_id = u.id
		WHERE u.id = ?`, id).Scan(
		&u.ID, &u.UserID, &cmsFileID, &u.SourcePath, &u.Filename, &u.Size, &u.Status,
		&key, &multipart, &sha, &u.ChunkSize, &u.ChunkCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("upload %s: %w", id, common.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	u.CMSFileID, u.ObjectKey, u.MultipartID, u.SHA256 = cmsFileID.String, key.String, multipart.String, sha.String
	return u, nil
}

// SetMultipartID records the storage multipart upload id.
func (s *Store) SetMultipartID(ctx context.Context, id common.UploadID, multipartID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE upload_manifests SET multipart_id = ?, updated_at = ? WHERE upload_id = ?`,
		multipartID, now(), id)
	return err
}

// SetStatus updates the upload status (and error message on failure).
func (s *Store) SetStatus(ctx context.Context, id common.UploadID, st common.UploadStatus, errMsg string) error {
	completed := sql.NullInt64{}
	if st == common.StatusCompleted {
		completed = sql.NullInt64{Int64: now(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE uploads SET status = ?, error = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		st, nullString(errMsg), completed, now(), id)
	return err
}

// MissingChunks returns chunks still needing upload (pending or inflight),
// ordered by index. This is the resume work-list.
func (s *Store) MissingChunks(ctx context.Context, id common.UploadID) ([]Chunk, error) {
	return s.queryChunks(ctx,
		`SELECT idx, offset_bytes, length_bytes, state, COALESCE(etag,''), COALESCE(sha256,''), retry_count
		 FROM chunks WHERE upload_id = ? AND state IN ('pending','inflight') ORDER BY idx`, id)
}

// AckedChunks returns chunks whose part is stored, ordered by index — the input
// to CompleteMultipart.
func (s *Store) AckedChunks(ctx context.Context, id common.UploadID) ([]Chunk, error) {
	return s.queryChunks(ctx,
		`SELECT idx, offset_bytes, length_bytes, state, COALESCE(etag,''), COALESCE(sha256,''), retry_count
		 FROM chunks WHERE upload_id = ? AND state = 'acked' ORDER BY idx`, id)
}

func (s *Store) queryChunks(ctx context.Context, q string, id common.UploadID) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Index, &c.Offset, &c.Length, &c.State, &c.ETag, &c.SHA256, &c.Retry); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkInFlight flips a chunk to inflight just before a worker sends it.
func (s *Store) MarkInFlight(ctx context.Context, id common.UploadID, idx int) error {
	return s.setChunkState(ctx, id, idx, common.StateInFlight)
}

// MarkAcked records a successful part upload with its ETag and per-chunk hash.
func (s *Store) MarkAcked(ctx context.Context, id common.UploadID, idx int, etag, sha256 string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chunks SET state = 'acked', etag = ?, sha256 = ?, updated_at = ? WHERE upload_id = ? AND idx = ?`,
		etag, sha256, now(), id, idx)
	return err
}

// MarkPending returns a failed chunk to pending and bumps its retry count.
func (s *Store) MarkPending(ctx context.Context, id common.UploadID, idx int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chunks SET state = 'pending', retry_count = retry_count + 1, updated_at = ? WHERE upload_id = ? AND idx = ?`,
		now(), id, idx)
	return err
}

// ResetInFlight moves any chunk left inflight (by a crash) back to pending.
// Called on recovery; deterministic (docs/ARCHITECTURE.md §8.2, §15).
func (s *Store) ResetInFlight(ctx context.Context, id common.UploadID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chunks SET state = 'pending', updated_at = ? WHERE upload_id = ? AND state = 'inflight'`,
		now(), id)
	return err
}

func (s *Store) setChunkState(ctx context.Context, id common.UploadID, idx int, st common.ChunkState) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chunks SET state = ?, updated_at = ? WHERE upload_id = ? AND idx = ?`, st, now(), id, idx)
	return err
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// DoneBytes sums the length of chunks already stored — the resume starting point
// for progress accounting.
func (s *Store) DoneBytes(ctx context.Context, id common.UploadID) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(length_bytes),0) FROM chunks WHERE upload_id = ? AND state IN ('acked','verified','skipped')`,
		id).Scan(&n)
	return n, err
}

// UpdateProgress writes the live speed/ETA fields shown in the UI.
func (s *Store) UpdateProgress(ctx context.Context, id common.UploadID, bytesDone, curBPS, avgBPS, etaSeconds int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE upload_manifests SET bytes_done = ?, cur_speed_bps = ?, avg_speed_bps = ?, eta_seconds = ?, updated_at = ?
		WHERE upload_id = ?`, bytesDone, curBPS, avgBPS, etaSeconds, now(), id)
	return err
}

// RecordStat appends a point to the transfer time-series (Transfer Statistics graphs).
func (s *Store) RecordStat(ctx context.Context, id common.UploadID, bytesDelta, speedBPS int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO transfer_statistics (upload_id, ts, bytes_delta, speed_bps) VALUES (?, ?, ?, ?)`,
		id, now(), bytesDelta, speedBPS)
	return err
}

// ListActive returns non-terminal uploads in scheduling order (priority desc,
// then oldest first). Drives the Manager and crash recovery.
func (s *Store) ListActive(ctx context.Context) ([]common.UploadID, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id FROM uploads u LEFT JOIN queue q ON q.upload_id = u.id
		WHERE u.status NOT IN ('completed','failed','canceled')
		ORDER BY COALESCE(q.priority,0) DESC, COALESCE(q.enqueued_at, u.created_at) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []common.UploadID
	for rows.Next() {
		var id common.UploadID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RunnableUploads returns uploads the supervisor may start now: non-terminal and
// not paused, in scheduling order. Paused uploads are excluded so a pause sticks
// until the user resumes; ListActive (used by crash recovery) keeps them.
func (s *Store) RunnableUploads(ctx context.Context) ([]common.UploadID, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id FROM uploads u LEFT JOIN queue q ON q.upload_id = u.id
		WHERE u.status NOT IN ('completed','failed','canceled','paused')
		ORDER BY COALESCE(q.priority,0) DESC, COALESCE(q.enqueued_at, u.created_at) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []common.UploadID
	for rows.Next() {
		var id common.UploadID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

const summaryCols = `u.id, u.filename, u.size_bytes, u.status, COALESCE(u.error,''),
	COALESCE(m.bytes_done,0), COALESCE(m.cur_speed_bps,0), COALESCE(m.avg_speed_bps,0),
	COALESCE(m.eta_seconds,0), u.updated_at`

func scanSummary(sc interface{ Scan(...any) error }) (Summary, error) {
	var s Summary
	err := sc.Scan(&s.ID, &s.Filename, &s.Size, &s.Status, &s.Error,
		&s.BytesDone, &s.CurBPS, &s.AvgBPS, &s.ETASeconds, &s.UpdatedAt)
	return s, err
}

// ListSummaries returns every upload's list view, newest activity first.
func (s *Store) ListSummaries(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+summaryCols+`
		FROM uploads u JOIN upload_manifests m ON m.upload_id = u.id
		ORDER BY u.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		sum, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// LoadSummary returns one upload's list view, or common.ErrNotFound.
func (s *Store) LoadSummary(ctx context.Context, id common.UploadID) (Summary, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+summaryCols+`
		FROM uploads u JOIN upload_manifests m ON m.upload_id = u.id
		WHERE u.id = ?`, id)
	sum, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, fmt.Errorf("upload %s: %w", id, common.ErrNotFound)
	}
	return sum, err
}

// SetSetting / GetSetting are the settings key/value store.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now())
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("setting %q: %w", key, common.ErrNotFound)
	}
	return v, err
}

// AppendEvent writes one tamper-evident audit record (SOC2). Each row's hash
// chains the previous one: hash = SHA256(prev_hash | ts | actor | kind | upload | payload).
// It runs in a transaction so the read-then-append is atomic against the single
// writer, keeping the chain linear.
func (s *Store) AppendEvent(ctx context.Context, actor, kind string, id common.UploadID, payload string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var prev string
	err = tx.QueryRowContext(ctx, `SELECT hash FROM events ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ts := now()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s|%s", prev, ts, actor, kind, id, payload)))
	hash := hex.EncodeToString(sum[:])
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (ts, actor, kind, upload_id, payload_json, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, nullString(actor), kind, nullString(string(id)), nullString(payload), prev, hash)
	if err != nil {
		return err
	}
	return tx.Commit()
}
