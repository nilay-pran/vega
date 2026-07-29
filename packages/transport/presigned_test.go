package transport_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	uploadserver "code.sli.ke/go/vega/apps/upload-server"
	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport"
)

const devToken = "presign-test-token"

// fakeSpaces is an ObjectStore that also presigns part uploads, standing in for
// S3Store without a real S3. PresignUploadPart points the client at s3URL, an
// in-test storage endpoint, so the direct upload path is exercised end to end.
type fakeSpaces struct {
	s3URL string

	mu        sync.Mutex
	completed []storage.Part
}

func (f *fakeSpaces) InitMultipart(context.Context, string) (string, error) { return "up-1", nil }

// UploadPart is never called on the presigned path (the client goes direct to
// storage); present only to satisfy the interface.
func (f *fakeSpaces) UploadPart(context.Context, string, string, int, io.Reader, int64) (storage.Part, error) {
	return storage.Part{}, fmt.Errorf("server-in-path upload should not happen on the presigned transport")
}

func (f *fakeSpaces) CompleteMultipart(_ context.Context, _, _ string, parts []storage.Part) error {
	f.mu.Lock()
	f.completed = parts
	f.mu.Unlock()
	return nil
}

func (f *fakeSpaces) AbortMultipart(context.Context, string, string) error { return nil }
func (f *fakeSpaces) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, storage.ErrNoSuchKey
}

func (f *fakeSpaces) PresignUploadPart(_ context.Context, key, uploadID string, partNumber int, expiry time.Duration) (storage.PresignedPut, error) {
	u := fmt.Sprintf("%s/put?key=%s&uploadId=%s&partNumber=%d",
		f.s3URL, url.QueryEscape(key), url.QueryEscape(uploadID), partNumber)
	return storage.PresignedPut{URL: u, Method: http.MethodPut, Expires: time.Now().Add(expiry)}, nil
}

var _ storage.PartPresigner = (*fakeSpaces)(nil)

// TestPresignedUploadGoesDirectToStorage checks that a part uploaded through the
// presigned transport lands at the storage endpoint (not the upload server),
// that its ETag threads back, and that the server never sees the bulk bytes.
func TestPresignedUploadGoesDirectToStorage(t *testing.T) {
	// Storage stand-in: records PUT bodies by part number, returns an ETag.
	var smu sync.Mutex
	stored := map[string][]byte{}
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "want PUT", http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("direct storage PUT carried Authorization %q; presigned URL must self-authenticate", got)
		}
		body, _ := io.ReadAll(r.Body)
		n := r.URL.Query().Get("partNumber")
		smu.Lock()
		stored[n] = body
		smu.Unlock()
		w.Header().Set("ETag", `"etag-`+n+`"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer s3.Close()

	fake := &fakeSpaces{s3URL: s3.URL}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier := auth.DevVerifier{Token: devToken, Subject: "tester"}
	server := uploadserver.New(fake, verifier, nil, log)
	ctrl := httptest.NewServer(server.Handler())
	defer ctrl.Close()

	store := transport.NewPresignedStore(ctrl.URL, devToken, ctrl.Client(), false)
	ctx := context.Background()

	id, err := store.InitMultipart(ctx, "clips/a.mp4")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	data := bytes.Repeat([]byte("slike"), 4096) // 20 KiB
	part, err := store.UploadPart(ctx, "clips/a.mp4", id, 1, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if part.ETag != "etag-1" {
		t.Errorf("ETag = %q, want %q (quotes should be trimmed)", part.ETag, "etag-1")
	}

	smu.Lock()
	got := stored["1"]
	smu.Unlock()
	if !bytes.Equal(got, data) {
		t.Fatalf("storage received %d bytes, want %d", len(got), len(data))
	}

	if err := store.CompleteMultipart(ctx, "clips/a.mp4", id, []storage.Part{part}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.completed) != 1 || fake.completed[0].ETag != "etag-1" {
		t.Errorf("completed parts = %+v, want one part with ETag etag-1", fake.completed)
	}
}

// TestPresignedFailsClearlyWithoutPresigner checks that when the backend cannot
// presign (e.g. the in-memory store), the presign route is absent and the
// transport surfaces a clean error rather than hanging or misbehaving.
func TestPresignedFailsClearlyWithoutPresigner(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier := auth.DevVerifier{Token: devToken, Subject: "tester"}
	server := uploadserver.New(storage.NewMemStore(), verifier, nil, log)
	ctrl := httptest.NewServer(server.Handler())
	defer ctrl.Close()

	store := transport.NewPresignedStore(ctrl.URL, devToken, ctrl.Client(), false)
	ctx := context.Background()
	id, err := store.InitMultipart(ctx, "k")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	_, err = store.UploadPart(ctx, "k", id, 1, bytes.NewReader([]byte("x")), 1)
	if err == nil {
		t.Fatal("expected an error when the backend cannot presign, got nil")
	}
}
