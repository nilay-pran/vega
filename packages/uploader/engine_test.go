package uploader

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/storage"
)

// flakyStore fails the first UploadPart for a chosen part number, then behaves
// normally — simulating a mid-transfer failure to exercise resume.
type flakyStore struct {
	*storage.MemStore
	mu       sync.Mutex
	failPart int
	tripped  bool
}

func (f *flakyStore) UploadPart(ctx context.Context, key, up string, n int, r io.Reader, size int64) (storage.Part, error) {
	f.mu.Lock()
	if n == f.failPart && !f.tripped {
		f.tripped = true
		f.mu.Unlock()
		_, _ = io.Copy(io.Discard, r) // drain so the reader/tee behaves like a real transfer
		return storage.Part{}, errors.New("injected failure")
	}
	f.mu.Unlock()
	return f.MemStore.UploadPart(ctx, key, up, n, r, size)
}

func TestEngineUploadResumeAndVerify(t *testing.T) {
	ctx := context.Background()

	// 12 MiB of random data -> three 5/5/2 MiB parts.
	content := make([]byte, 12<<20)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "movie.mov")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	wantSHA := sha256.Sum256(content)

	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := manifests.NewStore(db)
	mem := storage.NewMemStore()
	flaky := &flakyStore{MemStore: mem, failPart: 2} // fail part 2 once
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := New(store, flaky, log, Config{Workers: 3})

	id, err := eng.Enqueue(ctx, EnqueueReq{User: "u1", Email: "a@b.c", Path: src})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// First run must fail because part 2 was injected to fail.
	if err := eng.Run(ctx, id); err == nil {
		t.Fatal("expected first Run to fail")
	}
	u, _ := store.LoadUpload(ctx, id)
	if u.Status == common.StatusCompleted {
		t.Fatal("upload should not be completed after a failed run")
	}

	// Second run resumes and completes.
	if err := eng.Run(ctx, id); err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	u, _ = store.LoadUpload(ctx, id)
	if u.Status != common.StatusCompleted {
		t.Fatalf("status = %s, want completed", u.Status)
	}

	// The assembled object must byte-for-byte equal the source.
	rc, err := mem.Get(ctx, u.ObjectKey)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, content) {
		t.Fatalf("assembled object differs from source (%d vs %d bytes)", len(got), len(content))
	}
	if u.SHA256 != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("manifest sha mismatch")
	}
}
