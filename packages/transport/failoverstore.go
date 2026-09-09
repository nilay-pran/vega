package transport

import (
	"context"
	"fmt"
	"io"

	"code.sli.ke/go/vega/packages/storage"
)

// FailoverStore is the ObjectStore the engine actually drives when running in
// "auto" mode: a live, self-healing wrapper over several candidate transports.
// Every call feeds the shared Chooser's circuit breaker, so a path that starts
// failing mid-upload is skipped on the very next call — including the engine's
// own retry of the same chunk after a transient error — without the upload
// ever seeing anything worse than an error it already knows how to retry.
//
// InitMultipart, CompleteMultipart, AbortMultipart and Get additionally hop to
// the next candidate within the same call: their request bodies are small
// (a key, a part list) or absent, so resending them on a different path is
// safe. UploadPart's body is a large, single-pass stream handed in by the
// caller; once a byte of it has been read, it cannot be replayed to a second
// candidate, so a failed part is reported to the breaker and returned as-is —
// the engine already re-issues that chunk with a fresh reader (see
// packages/uploader.Engine.uploadChunk), and by then Ordered has moved the
// failed path out of the way.
//
// Reusing the same multipartID across candidates is safe for the single
// upload-server process this ships today: h3+http share one Server/owner-table
// instance (apps/upload-server/cmd/upload-server-h3), and SLKT enforces no
// ownership of its own, trusting whichever ID it is given
// (packages/transport/slkt/server.go). docs/ARCHITECTURE.md §7/P7's
// horizontally-scaled, multi-instance server is the case this does not yet
// cover, since ownership there needs to move to shared storage first.
type FailoverStore struct {
	chooser *Chooser
	stores  map[string]storage.ObjectStore
}

func NewFailoverStore(chooser *Chooser, candidates ...Candidate) *FailoverStore {
	stores := make(map[string]storage.ObjectStore, len(candidates))
	for _, c := range candidates {
		stores[c.Name] = c.Store
	}
	return &FailoverStore{chooser: chooser, stores: stores}
}

var _ storage.ObjectStore = (*FailoverStore)(nil)

func (f *FailoverStore) InitMultipart(ctx context.Context, key string) (string, error) {
	var id string
	err := f.withFailover(ctx, "init multipart", func(s storage.ObjectStore) error {
		var err error
		id, err = s.InitMultipart(ctx, key)
		return err
	})
	return id, err
}

// UploadPart tries only the current best candidate: its reader is a single-pass
// stream that cannot be safely replayed against a second path within one call.
// A failure trips that path's breaker and is returned to the caller, which
// (per packages/uploader.Engine.uploadChunk) marks the chunk pending and lets
// the next attempt — on the now-deprioritized path's replacement — pick it up.
func (f *FailoverStore) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (storage.Part, error) {
	cands := f.chooser.Ordered()
	if len(cands) == 0 {
		return storage.Part{}, errNoCandidates
	}
	cand := cands[0]
	store, ok := f.stores[cand.Name]
	if !ok {
		return storage.Part{}, fmt.Errorf("upload part: no store registered for path %q", cand.Name)
	}
	part, err := store.UploadPart(ctx, key, uploadID, partNumber, r, size)
	if err != nil {
		f.chooser.ReportFailure(cand.Name, err)
		return storage.Part{}, fmt.Errorf("upload part via %s: %w", cand.Name, err)
	}
	f.chooser.ReportSuccess(cand.Name)
	return part, nil
}

func (f *FailoverStore) CompleteMultipart(ctx context.Context, key, uploadID string, parts []storage.Part) error {
	return f.withFailover(ctx, "complete multipart", func(s storage.ObjectStore) error {
		return s.CompleteMultipart(ctx, key, uploadID, parts)
	})
}

func (f *FailoverStore) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return f.withFailover(ctx, "abort multipart", func(s storage.ObjectStore) error {
		return s.AbortMultipart(ctx, key, uploadID)
	})
}

func (f *FailoverStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := f.withFailover(ctx, "get object", func(s storage.ObjectStore) error {
		var err error
		rc, err = s.Get(ctx, key)
		return err
	})
	return rc, err
}

// withFailover tries every candidate in current health order until one
// succeeds, reporting each attempt to the breaker as it goes. Only safe for
// calls with no partially-consumed request body — see UploadPart's own
// (non-hopping) handling above.
func (f *FailoverStore) withFailover(ctx context.Context, op string, fn func(storage.ObjectStore) error) error {
	cands := f.chooser.Ordered()
	if len(cands) == 0 {
		return errNoCandidates
	}
	var lastErr error
	for _, cand := range cands {
		store, ok := f.stores[cand.Name]
		if !ok {
			continue
		}
		if err := fn(store); err != nil {
			f.chooser.ReportFailure(cand.Name, err)
			lastErr = fmt.Errorf("%s via %s: %w", op, cand.Name, err)
			if ctx.Err() != nil {
				return lastErr // caller canceled/timed out; stop hopping paths
			}
			continue
		}
		f.chooser.ReportSuccess(cand.Name)
		return nil
	}
	return lastErr
}
