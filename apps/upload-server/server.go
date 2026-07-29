// Package uploadserver is the upload server's inbound HTTP adapter. It exposes
// the multipart operations the engine needs, authenticates every request, and
// enforces per-upload ownership. Its backing store is any storage.ObjectStore
// (Spaces in prod, a fake in tests). The binary SLKT terminator will be a second
// inbound adapter over the same ObjectStore + ownership core.
//
// Deliberately minimal for v1: ownership is tracked in memory. Production moves
// it to the shared Postgres store so any instance can resume/assemble
// (docs/ARCHITECTURE.md §11.2, §12); the OwnerStore interface is the seam.
package uploadserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.sli.ke/go/vega/packages/auth"
	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport"
)

// OwnerStore records which subject owns which multipart upload. Swap the
// in-memory implementation for Postgres to get cross-instance resume.
type OwnerStore interface {
	Put(multipartID, subject, key string)
	Check(multipartID, subject string) error
	Delete(multipartID string)
}

var errForbidden = errors.New("forbidden")

type memOwners struct {
	mu     sync.Mutex
	owners map[string]string // multipartID -> subject
}

func NewMemOwners() *memOwners { return &memOwners{owners: make(map[string]string)} }

func (m *memOwners) Put(id, subject, _ string) {
	m.mu.Lock()
	m.owners[id] = subject
	m.mu.Unlock()
}

func (m *memOwners) Check(id, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, ok := m.owners[id]
	if !ok || owner != subject {
		return errForbidden
	}
	return nil
}

func (m *memOwners) Delete(id string) {
	m.mu.Lock()
	delete(m.owners, id)
	m.mu.Unlock()
}

// presignTTL bounds how long a direct-to-storage part URL stays valid. Long
// enough to upload one large part on a slow link, short enough that a leaked URL
// is quickly useless.
const presignTTL = 15 * time.Minute

type Server struct {
	objects   storage.ObjectStore
	presigner storage.PartPresigner // non-nil iff objects can hand out direct URLs
	auth      auth.Verifier
	owners    OwnerStore
	log       *slog.Logger
}

func New(objects storage.ObjectStore, verifier auth.Verifier, owners OwnerStore, log *slog.Logger) *Server {
	if owners == nil {
		owners = NewMemOwners()
	}
	pp, _ := objects.(storage.PartPresigner)
	return &Server{objects: objects, presigner: pp, auth: verifier, owners: owners, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/multipart", s.handleInit)
	mux.HandleFunc("PUT /v1/multipart/{id}/parts/{n}", s.handleUploadPart)
	mux.HandleFunc("POST /v1/multipart/{id}/complete", s.handleComplete)
	mux.HandleFunc("DELETE /v1/multipart/{id}", s.handleAbort)
	mux.HandleFunc("GET /v1/object", s.handleGet)
	// The presign route exists only when the backing store can sign direct
	// uploads (S3/Spaces). With the in-memory backend it stays absent, so the
	// presigned transport degrades to a clean 404 rather than a broken half-path.
	if s.presigner != nil {
		mux.HandleFunc("POST /v1/multipart/{id}/parts/{n}/presign", s.handlePresignPart)
	}
	return s.withAuth(mux)
}

type ctxKey int

const subjectKey ctxKey = 0

func subjectOf(ctx context.Context) string {
	s, _ := ctx.Value(subjectKey).(string)
	return s
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reachability probe: unauthenticated, so the transport chooser can test
		// the path before it holds a token.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/ping" {
			w.WriteHeader(http.StatusOK)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		subject, err := s.auth.Verify(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), subjectKey, subject)))
	})
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	var req transport.InitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	mp, err := s.objects.InitMultipart(r.Context(), req.Key)
	if err != nil {
		s.fail(w, "init multipart", err)
		return
	}
	s.owners.Put(mp, subjectOf(r.Context()), req.Key)
	writeJSON(w, transport.InitResponse{MultipartID: mp})
}

func (s *Server) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.owners.Check(id, subjectOf(r.Context())); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "bad part number", http.StatusBadRequest)
		return
	}
	key := r.URL.Query().Get("key")
	part, err := s.objects.UploadPart(r.Context(), key, id, n, r.Body, r.ContentLength)
	if err != nil {
		s.fail(w, "upload part", err)
		return
	}
	writeJSON(w, transport.PartResponse{PartNumber: part.PartNumber, ETag: part.ETag})
}

// handlePresignPart returns a direct-to-storage URL for one part so the client
// uploads the bytes to S3/Spaces itself, keeping the upload server off the bulk
// path. Ownership is still enforced here — the presign is the only server touch
// point for the part, so it is where authorization must hold.
func (s *Server) handlePresignPart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.owners.Check(id, subjectOf(r.Context())); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "bad part number", http.StatusBadRequest)
		return
	}
	key := r.URL.Query().Get("key")
	put, err := s.presigner.PresignUploadPart(r.Context(), key, id, n, presignTTL)
	if err != nil {
		s.fail(w, "presign part", err)
		return
	}
	writeJSON(w, transport.PresignResponse{URL: put.URL, Method: put.Method})
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.owners.Check(id, subjectOf(r.Context())); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req transport.CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	parts := make([]storage.Part, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = storage.Part{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	if err := s.objects.CompleteMultipart(r.Context(), req.Key, id, parts); err != nil {
		s.fail(w, "complete multipart", err)
		return
	}
	s.owners.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.owners.Check(id, subjectOf(r.Context())); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.objects.AbortMultipart(r.Context(), r.URL.Query().Get("key"), id); err != nil {
		s.fail(w, "abort multipart", err)
		return
	}
	s.owners.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	rc, err := s.objects.Get(r.Context(), r.URL.Query().Get("key"))
	if errors.Is(err, storage.ErrNoSuchKey) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.fail(w, "get object", err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, rc)
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.log.Error("request failed", "op", what, "err", err)
	http.Error(w, what+": "+err.Error(), http.StatusBadGateway)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
