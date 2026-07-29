package manifests

import (
	"context"
	"path/filepath"
	"testing"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
)

func newStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	if err := s.UpsertUser(ctx, "u1", "u1@example.com"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func makeUpload(t *testing.T, s *Store, ctx context.Context, id common.UploadID, chunks []Chunk) {
	t.Helper()
	u := &Upload{ID: id, UserID: "u1", SourcePath: "/x", Filename: "x", Size: 10, Status: common.StatusReady, ObjectKey: "k/" + string(id), ChunkSize: MinPartSize, ChunkCount: len(chunks)}
	if err := s.CreateUpload(ctx, u, chunks); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, ctx := newStore(t)
	if _, err := s.GetSetting(ctx, "missing"); err == nil {
		t.Fatal("expected ErrNotFound for missing setting")
	}
	if err := s.SetSetting(ctx, "bandwidth_cap", "100"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "bandwidth_cap", "200"); err != nil { // upsert
		t.Fatal(err)
	}
	v, err := s.GetSetting(ctx, "bandwidth_cap")
	if err != nil || v != "200" {
		t.Fatalf("got %q, %v; want 200", v, err)
	}
}

func TestListActiveExcludesTerminal(t *testing.T) {
	s, ctx := newStore(t)
	makeUpload(t, s, ctx, "a", nil)
	makeUpload(t, s, ctx, "b", nil)
	makeUpload(t, s, ctx, "c", nil)
	if err := s.SetStatus(ctx, "b", common.StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("active = %v, want 2 (a,c)", ids)
	}
}

func TestDoneBytes(t *testing.T) {
	s, ctx := newStore(t)
	chunks := []Chunk{
		{Index: 0, Offset: 0, Length: 100, State: common.StatePending},
		{Index: 1, Offset: 100, Length: 50, State: common.StatePending},
	}
	makeUpload(t, s, ctx, "u", chunks)
	if err := s.MarkAcked(ctx, "u", 0, "etag0", "sha0"); err != nil {
		t.Fatal(err)
	}
	n, err := s.DoneBytes(ctx, "u")
	if err != nil || n != 100 {
		t.Fatalf("done = %d, %v; want 100", n, err)
	}
}

func TestAppendEventChain(t *testing.T) {
	s, ctx := newStore(t)
	for _, k := range []string{"upload.created", "upload.completed"} {
		if err := s.AppendEvent(ctx, "u1", k, "up1", "payload"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT prev_hash, hash FROM events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prevHashes, hashes []string
	for rows.Next() {
		var prev, h string
		if err := rows.Scan(&prev, &h); err != nil {
			t.Fatal(err)
		}
		prevHashes = append(prevHashes, prev)
		hashes = append(hashes, h)
	}
	if len(hashes) != 2 {
		t.Fatalf("want 2 events, got %d", len(hashes))
	}
	if prevHashes[0] != "" {
		t.Fatal("first event prev_hash should be empty")
	}
	if prevHashes[1] != hashes[0] {
		t.Fatal("chain broken: event 2 prev_hash != event 1 hash")
	}
}
