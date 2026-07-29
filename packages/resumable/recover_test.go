package resumable

import (
	"context"
	"path/filepath"
	"testing"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
)

func TestRecoverResetsInterruptedUpload(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := manifests.NewStore(db)
	if err := s.UpsertUser(ctx, "u1", "u1@x.y"); err != nil {
		t.Fatal(err)
	}

	// Simulate a process that died mid-upload: status "uploading", one chunk
	// stuck "inflight", one already "acked".
	chunks := []manifests.Chunk{
		{Index: 0, Offset: 0, Length: 5, State: common.StateAcked},
		{Index: 1, Offset: 5, Length: 5, State: common.StateInFlight},
	}
	u := &manifests.Upload{ID: "up1", UserID: "u1", SourcePath: "/x", Filename: "x", Size: 10, Status: common.StatusUploading, ObjectKey: "k", ChunkSize: 5, ChunkCount: 2}
	if err := s.CreateUpload(ctx, u, chunks); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "up1", common.StatusUploading, ""); err != nil {
		t.Fatal(err)
	}

	n, err := Recover(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered %d, want 1", n)
	}

	// The inflight chunk is now pending again; the acked one is untouched.
	missing, err := s.MissingChunks(ctx, "up1")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Index != 1 || missing[0].State != common.StatePending {
		t.Fatalf("missing = %+v, want only chunk 1 pending", missing)
	}
	got, _ := s.LoadUpload(ctx, "up1")
	if got.Status != common.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
}
