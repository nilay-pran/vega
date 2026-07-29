package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestMemStoreMultipartRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	id, err := s.InitMultipart(ctx, "videos/a.mov")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// Upload three parts out of order to prove ordering is by part number.
	want := []byte("HELLO-WORLD-1234567890")
	segments := map[int][]byte{1: []byte("HELLO-"), 2: []byte("WORLD-"), 3: []byte("1234567890")}
	var parts []Part
	for _, n := range []int{3, 1, 2} {
		p, err := s.UploadPart(ctx, "videos/a.mov", id, n, bytes.NewReader(segments[n]), int64(len(segments[n])))
		if err != nil {
			t.Fatalf("part %d: %v", n, err)
		}
		parts = append(parts, p)
	}

	if err := s.CompleteMultipart(ctx, "videos/a.mov", id, parts); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rc, err := s.Get(ctx, "videos/a.mov")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, want) {
		t.Fatalf("assembled = %q, want %q", got, want)
	}
}

func TestMemStoreGetMissing(t *testing.T) {
	if _, err := NewMemStore().Get(context.Background(), "nope"); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("err = %v, want ErrNoSuchKey", err)
	}
}
