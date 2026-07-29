// Package storage is the object-storage port plus its adapters.
//
// Every upload is exactly one multipart upload and a protocol chunk is one
// multipart part (docs/ARCHITECTURE.md §13). Assembly is therefore identical
// no matter which transport delivered a part, and this interface is all the
// engine and the server need to know about storage.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNoSuchKey is returned by Get when an object does not exist.
var ErrNoSuchKey = errors.New("storage: no such key")

// PresignedPut is a short-lived, pre-authenticated URL a client can PUT one
// multipart part to directly, without the part bytes passing through the upload
// server. The URL already carries its own authentication (an S3 SigV4 query
// signature), so the client sends no bearer token to it.
type PresignedPut struct {
	URL     string    `json:"url"`
	Method  string    `json:"method"`
	Expires time.Time `json:"expires"`
}

// PartPresigner is an optional capability an ObjectStore may implement to hand
// out direct-to-storage upload URLs. When the backing store is S3/Spaces the
// server can offload bulk part bytes onto the client→storage path and only sign
// requests; stores that cannot (e.g. the in-memory fake) simply do not
// implement it, and the server keeps the bytes on its own path.
type PartPresigner interface {
	PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int, expiry time.Duration) (PresignedPut, error)
}

// Part identifies one completed multipart part. PartNumber is 1-based, matching
// the S3 multipart API (our internal chunk index is 0-based; the +1 mapping
// happens at this boundary and nowhere else).
type Part struct {
	PartNumber int
	ETag       string
}

// ObjectStore is the abstraction over DigitalOcean Spaces (S3) and any future
// object store. Implementations must be safe for concurrent use.
type ObjectStore interface {
	InitMultipart(ctx context.Context, key string) (uploadID string, err error)
	UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (Part, error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
	AbortMultipart(ctx context.Context, key, uploadID string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}
