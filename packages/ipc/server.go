package ipc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"

	"code.sli.ke/go/vega/packages/common"
)

// Backend is what the daemon plugs into the server: the upload operations a
// controller can invoke. It is deliberately small — the server owns transport,
// auth, and encoding; the daemon owns behavior.
type Backend interface {
	Enqueue(ctx context.Context, req EnqueueRequest) (common.UploadID, error)
	List(ctx context.Context) ([]UploadView, error)
	Get(ctx context.Context, id common.UploadID) (UploadView, error)
	Pause(ctx context.Context, id common.UploadID) error
	Resume(ctx context.Context, id common.UploadID) error
	Cancel(ctx context.Context, id common.UploadID) error
	Metrics(ctx context.Context) map[string]int64
}

// Server exposes a Backend over a Unix socket as HTTP+JSON. Live events come
// from the shared EventBus and stream to controllers as server-sent events.
type Server struct {
	backend Backend
	bus     *common.EventBus
	token   string
	log     *slog.Logger
	http    *http.Server
}

func NewServer(backend Backend, bus *common.EventBus, token string, log *slog.Logger) *Server {
	s := &Server{backend: backend, bus: bus, token: token, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/uploads", s.enqueue)
	mux.HandleFunc("GET /v1/uploads", s.list)
	mux.HandleFunc("GET /v1/uploads/{id}", s.get)
	mux.HandleFunc("POST /v1/uploads/{id}/pause", s.action((*Server).pauseFn))
	mux.HandleFunc("POST /v1/uploads/{id}/resume", s.action((*Server).resumeFn))
	mux.HandleFunc("POST /v1/uploads/{id}/cancel", s.action((*Server).cancelFn))
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("GET /v1/metrics", s.metrics)
	s.http = &http.Server{Handler: s.auth(mux)}
	return s
}

// Serve listens on the Unix socket until the context is canceled. It removes a
// stale socket file left by an unclean shutdown before binding.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = s.http.Close()
	}()
	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// auth enforces the bearer token on every request. The SSE stream is included:
// a controller must prove itself before it can observe upload activity.
func (s *Server) auth(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, "unauthorized")
	})
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	var req EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	id, err := s.backend.Enqueue(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, IDResponse{ID: string(id)})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	views, err := s.backend.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if views == nil {
		views = []UploadView{}
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	view, err := s.backend.Get(r.Context(), common.UploadID(r.PathValue("id")))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// action adapts a one-argument backend call (pause/resume/cancel) into a handler.
func (s *Server) action(fn func(*Server, context.Context, common.UploadID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(s, r.Context(), common.UploadID(r.PathValue("id"))); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) pauseFn(ctx context.Context, id common.UploadID) error {
	return s.backend.Pause(ctx, id)
}
func (s *Server) resumeFn(ctx context.Context, id common.UploadID) error {
	return s.backend.Resume(ctx, id)
}
func (s *Server) cancelFn(ctx context.Context, id common.UploadID) error {
	return s.backend.Cancel(ctx, id)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Metrics(r.Context()))
}

// events streams engine events as SSE until the controller disconnects. A slow
// controller only misses events (the bus drops for full buffers); it can never
// stall the engine.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	id, ch := s.bus.Subscribe(256)
	defer s.bus.Unsubscribe(id)
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if err := enc.Encode(ev); err != nil { // Encode writes the trailing \n
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// fail maps a backend error to a status: not-found is 404, everything else 500.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, common.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.log.Warn("request failed", "err", err)
	writeErr(w, http.StatusInternalServerError, err.Error())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorResponse{Error: msg})
}
