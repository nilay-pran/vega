package uploadserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/database"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport"
	"code.sli.ke/go/vega/packages/uploader"
)

// End to end across the process boundary: the real engine uploads a real file
// through the HTTP server into the server's object store, then verifies it.
func TestEndToEndThroughServer(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	mem := storage.NewMemStore()
	srv := New(mem, auth.DevVerifier{Token: "secret", Subject: "u1"}, NewMemOwners(), log)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	content := make([]byte, 11<<20) // 11 MiB -> 5/5/1 MiB parts
	rand.Read(content)
	src := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	hs := transport.NewHTTPStore(ts.URL, "secret", ts.Client())
	eng := uploader.New(manifests.NewStore(db), hs, log, uploader.Config{Workers: 3})

	id, err := eng.Enqueue(ctx, uploader.EnqueueReq{User: "u1", Email: "u1@example.com", Path: src})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := eng.Run(ctx, id); err != nil {
		t.Fatalf("run: %v", err)
	}

	u, _ := manifests.NewStore(db).LoadUpload(ctx, id)
	if u.Status != common.StatusCompleted {
		t.Fatalf("status = %s, want completed", u.Status)
	}
	rc, err := mem.Get(ctx, u.ObjectKey)
	if err != nil {
		t.Fatalf("object missing: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, content) {
		t.Fatal("assembled object differs from source")
	}
}

func TestRejectsBadToken(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(storage.NewMemStore(), auth.DevVerifier{Token: "secret", Subject: "u1"}, NewMemOwners(), log)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	hs := transport.NewHTTPStore(ts.URL, "wrong", ts.Client())
	if _, err := hs.InitMultipart(context.Background(), "k"); err == nil {
		t.Fatal("expected auth failure with wrong token")
	}
}
