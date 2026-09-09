package uploader

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/manifests"
)

// backoffBase and backoffMax bound the delay before a failed run is retried:
// exponential from backoffBase, capped at backoffMax. Without this, a run that
// keeps failing outright (e.g. every transport path is down at once) would
// have its finishing goroutine call Kick and relaunch immediately, forever —
// busy-looping the supervisor instead of giving the transport layer's own
// circuit breaker (packages/transport.Chooser) time for its cooldown to
// matter or a probe tick to detect recovery.
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 30 * time.Second
)

// backoff returns the delay before retrying a run that has now failed fails
// times in a row.
func backoff(fails int) time.Duration {
	if fails < 1 {
		fails = 1
	}
	if fails > 8 { // pow2 growth already exceeds backoffMax well before this
		fails = 8
	}
	d := backoffBase * time.Duration(int64(math.Pow(2, float64(fails-1))))
	if d > backoffMax {
		return backoffMax
	}
	return d
}

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

	mu         sync.Mutex
	running    map[common.UploadID]context.CancelFunc
	retryAfter map[common.UploadID]time.Time // backoff floor after a failed run
	failCount  map[common.UploadID]int       // consecutive failed runs, drives backoff
	closed     bool
	wg         sync.WaitGroup
}

func NewSupervisor(base context.Context, store *manifests.Store, engine *Engine, log *slog.Logger, max int) *Supervisor {
	if max < 1 {
		max = 40
	}
	return &Supervisor{
		base:       base,
		store:      store,
		engine:     engine,
		log:        log,
		max:        max,
		running:    make(map[common.UploadID]context.CancelFunc),
		retryAfter: make(map[common.UploadID]time.Time),
		failCount:  make(map[common.UploadID]int),
	}
}

// Kick starts runnable uploads until either the concurrency limit or the queue
// is exhausted. It is safe to call from anywhere — after recovery, on a new
// enqueue, or when a running upload finishes — and is a no-op once Stopped. An
// upload whose last run failed is skipped until its backoff elapses, even
// though RunnableUploads still lists it (SQLite has no notion of the
// in-memory backoff clock).
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
		if _, ok := s.running[id]; ok {
			continue
		}
		if until, ok := s.retryAfter[id]; ok && time.Now().Before(until) {
			continue
		}
		s.launch(id)
	}
}

// launch runs one upload; caller must hold s.mu.
func (s *Supervisor) launch(id common.UploadID) {
	ctx, cancel := context.WithCancel(s.base)
	s.running[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.engine.Run(ctx, id)
		// ctx.Err() != nil means we were paused/canceled/stopped, not a failure.
		canceled := ctx.Err() != nil
		if err != nil && !canceled {
			s.log.Warn("upload run failed", "upload", id, "err", err)
		}

		s.mu.Lock()
		cancel()
		delete(s.running, id)
		var delay time.Duration
		if err != nil && !canceled {
			s.failCount[id]++
			delay = backoff(s.failCount[id])
			s.retryAfter[id] = time.Now().Add(delay)
		} else {
			delete(s.failCount, id)
			delete(s.retryAfter, id)
		}
		s.mu.Unlock()

		if delay > 0 {
			// Nothing else may ever call Kick again for this upload (it can be
			// the only thing in the queue), so schedule the retry ourselves.
			time.AfterFunc(delay, s.Kick)
		}
		s.Kick() // a slot freed now — pull whatever else is already runnable
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
