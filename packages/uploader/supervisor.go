package uploader

import (
	"context"
	"log/slog"
	"sync"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/manifests"
)

// Supervisor is the long-lived queue driver a daemon runs: it keeps up to max
// uploads running at once, backfilling a slot the moment one finishes, and
// exposes pause/resume/cancel. Unlike Manager (which drains the queue once and
// returns), the Supervisor stays up and reacts to new work via Kick. All state
// still lives in SQLite, so a restarted Supervisor resumes exactly where it was.
type Supervisor struct {
	base   context.Context
	store  *manifests.Store
	engine *Engine
	log    *slog.Logger
	max    int

	mu      sync.Mutex
	running map[common.UploadID]context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
}

func NewSupervisor(base context.Context, store *manifests.Store, engine *Engine, log *slog.Logger, max int) *Supervisor {
	if max < 1 {
		max = 40
	}
	return &Supervisor{
		base:    base,
		store:   store,
		engine:  engine,
		log:     log,
		max:     max,
		running: make(map[common.UploadID]context.CancelFunc),
	}
}

// Kick starts runnable uploads until either the concurrency limit or the queue
// is exhausted. It is safe to call from anywhere — after recovery, on a new
// enqueue, or when a running upload finishes — and is a no-op once Stopped.
func (s *Supervisor) Kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.running) >= s.max {
		return
	}
	ids, err := s.store.RunnableUploads(s.base)
	if err != nil {
		s.log.Warn("supervisor scan failed", "err", err)
		return
	}
	for _, id := range ids {
		if len(s.running) >= s.max {
			break
		}
		if _, ok := s.running[id]; !ok {
			s.launch(id)
		}
	}
}

// launch runs one upload; caller must hold s.mu.
func (s *Supervisor) launch(id common.UploadID) {
	ctx, cancel := context.WithCancel(s.base)
	s.running[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// ctx.Err() != nil means we were paused/canceled/stopped, not a failure.
		if err := s.engine.Run(ctx, id); err != nil && ctx.Err() == nil {
			s.log.Warn("upload run failed", "upload", id, "err", err)
		}
		s.mu.Lock()
		cancel()
		delete(s.running, id)
		s.mu.Unlock()
		s.Kick() // a slot freed — pull the next queued upload
	}()
}

// stop cancels any in-flight run for id. The status must already be set to a
// non-runnable state first, so the finishing goroutine's Kick cannot relaunch it.
func (s *Supervisor) stop(id common.UploadID) {
	s.mu.Lock()
	if cancel, ok := s.running[id]; ok {
		cancel()
	}
	s.mu.Unlock()
}

// Pause stops an in-flight upload and leaves it resumable. Setting Paused before
// canceling closes the relaunch race: RunnableUploads excludes paused, so the
// finishing goroutine cannot pick it back up.
func (s *Supervisor) Pause(ctx context.Context, id common.UploadID) error {
	if err := s.store.SetStatus(ctx, id, common.StatusPaused, ""); err != nil {
		return err
	}
	s.stop(id)
	return nil
}

// Resume marks a paused upload Ready and kicks the queue.
func (s *Supervisor) Resume(ctx context.Context, id common.UploadID) error {
	if err := s.store.SetStatus(ctx, id, common.StatusReady, ""); err != nil {
		return err
	}
	s.Kick()
	return nil
}

// Cancel stops an upload for good and aborts its storage multipart. Canceled is
// set first (same relaunch-race reasoning as Pause) and Abort is idempotent.
func (s *Supervisor) Cancel(ctx context.Context, id common.UploadID) error {
	if err := s.store.SetStatus(ctx, id, common.StatusCanceled, ""); err != nil {
		return err
	}
	s.stop(id)
	return s.engine.Abort(ctx, id)
}

// Stop cancels every running upload and waits for them to unwind. Uploads are
// left resumable; nothing is failed. Call it on daemon shutdown.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.running {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
