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

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/telemetry"
)

func TestManagerRunsQueueConcurrently(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mgr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	store := manifests.NewStore(db)
	mem := storage.NewMemStore()
	bus := common.NewEventBus()
	metrics := telemetry.NewMetrics()
	eng := New(store, mem, log, Config{Workers: 3, Bus: bus, Metrics: metrics})

	// Enqueue several files of varying sizes.
	const n = 6
	dir := t.TempDir()
	sizes := []int{1 << 20, 7 << 20, 3 << 20, 11 << 20, 2 << 20, 6 << 20}
	ids := make([]common.UploadID, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, sizes[i])
		rand.Read(buf)
		p := filepath.Join(dir, fmt.Sprintf("f%d.bin", i))
		if err := os.WriteFile(p, buf, 0o600); err != nil {
			t.Fatal(err)
		}
		id, err := eng.Enqueue(ctx, EnqueueReq{User: "u1", Email: "u1@x.y", Path: p})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}

	mgr := NewManager(store, eng, 3) // at most 3 uploads at once
	attempted, err := mgr.RunQueue(ctx)
	if err != nil {
		t.Fatalf("run queue: %v", err)
	}
	if len(attempted) != n {
		t.Fatalf("attempted %d, want %d", len(attempted), n)
	}

	for _, id := range ids {
		u, _ := store.LoadUpload(ctx, id)
		if u.Status != common.StatusCompleted {
			t.Fatalf("upload %s status = %s, want completed", id, u.Status)
		}
	}
	if got := metrics.Snapshot()["uploads_completed"]; got != n {
		t.Fatalf("uploads_completed = %d, want %d", got, n)
	}
}
