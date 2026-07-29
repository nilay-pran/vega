package uploader

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/telemetry"
)

func newEngine(t *testing.T) (*manifests.Store, *Engine, context.Context) {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "sup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := manifests.NewStore(db)
	eng := New(store, storage.NewMemStore(), log, Config{Workers: 3, Bus: common.NewEventBus(), Metrics: telemetry.NewMetrics()})
	return store, eng, ctx
}

func enqueueFile(t *testing.T, eng *Engine, ctx context.Context, size int) common.UploadID {
	t.Helper()
	buf := make([]byte, size)
	rand.Read(buf)
	p := filepath.Join(t.TempDir(), fmt.Sprintf("f%d.bin", size))
	if err := os.WriteFile(p, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := eng.Enqueue(ctx, EnqueueReq{User: "u1", Email: "u1@x.y", Path: p})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func waitStatus(t *testing.T, store *manifests.Store, ctx context.Context, id common.UploadID, want common.UploadStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		u, err := store.LoadUpload(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if u.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	u, _ := store.LoadUpload(ctx, id)
	t.Fatalf("upload %s status = %s, want %s", id, u.Status, want)
}

// The supervisor drains the whole queue and backfills slots as uploads finish.
func TestSupervisorDrainsQueue(t *testing.T) {
	store, eng, ctx := newEngine(t)
	var ids []common.UploadID
	for i := 0; i < 5; i++ {
		ids = append(ids, enqueueFile(t, eng, ctx, (i+1)<<20))
	}
	sup := NewSupervisor(ctx, store, eng, slog.Default(), 2) // only 2 at a time
	defer sup.Stop()
	sup.Kick()
	for _, id := range ids {
		waitStatus(t, store, ctx, id, common.StatusCompleted)
	}
}

// Pause stops an upload and it stays paused (the finishing goroutine's Kick must
// not relaunch it); Resume brings it back to completion.
func TestSupervisorPauseResume(t *testing.T) {
	store, eng, ctx := newEngine(t)
	id := enqueueFile(t, eng, ctx, 40<<20) // big enough to still be running when paused
	sup := NewSupervisor(ctx, store, eng, slog.Default(), 4)
	defer sup.Stop()
	sup.Kick()

	if err := sup.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, store, ctx, id, common.StatusPaused)

	// Give the relaunch race a chance to misfire: status must remain paused.
	time.Sleep(100 * time.Millisecond)
	u, _ := store.LoadUpload(ctx, id)
	if u.Status != common.StatusPaused {
		t.Fatalf("after pause, status = %s, want paused", u.Status)
	}

	if err := sup.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, store, ctx, id, common.StatusCompleted)
}

// Cancel stops an upload for good and marks it canceled.
func TestSupervisorCancel(t *testing.T) {
	store, eng, ctx := newEngine(t)
	id := enqueueFile(t, eng, ctx, 40<<20)
	sup := NewSupervisor(ctx, store, eng, slog.Default(), 4)
	defer sup.Stop()
	sup.Kick()

	if err := sup.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, store, ctx, id, common.StatusCanceled)

	time.Sleep(100 * time.Millisecond)
	u, _ := store.LoadUpload(ctx, id)
	if u.Status != common.StatusCanceled {
		t.Fatalf("after cancel, status = %s, want canceled", u.Status)
	}
}
