package uploader

import (
	"context"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/manifests"
	"code.sli.ke/go/vega/packages/scheduler"
)

// Manager is the background engine: it drives the persistent queue, running many
// uploads concurrently (the "40+ simultaneous uploads" requirement) while each
// upload internally parallelizes its own chunks. It is the piece a daemon or the
// desktop controller drives; every upload's state lives in SQLite, so the
// Manager is stateless and safe to stop and restart.
type Manager struct {
	store         *manifests.Store
	engine        *Engine
	maxConcurrent int
}

func NewManager(store *manifests.Store, engine *Engine, maxConcurrent int) *Manager {
	if maxConcurrent < 1 {
		maxConcurrent = 40
	}
	return &Manager{store: store, engine: engine, maxConcurrent: maxConcurrent}
}

// RunQueue processes every currently-active upload, up to maxConcurrent at once,
// and returns the ids it attempted. Each upload runs through the resumable
// engine, so an upload interrupted on a previous pass continues from its missing
// set. Call it after resumable.Recover on startup, and again whenever new work
// is enqueued.
func (m *Manager) RunQueue(ctx context.Context) ([]common.UploadID, error) {
	ids, err := m.store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	tasks := make([]scheduler.Task, len(ids))
	for i, id := range ids {
		tasks[i] = func(ctx context.Context) error { return m.engine.Run(ctx, id) }
	}
	// One upload failing must not abort the others, so swallow per-upload errors
	// here (the engine already records them as failed/resumable state).
	wrapped := make([]scheduler.Task, len(tasks))
	for i, t := range tasks {
		wrapped[i] = func(ctx context.Context) error {
			if err := t(ctx); err != nil {
				m.engine.log.Warn("upload run failed", "upload", ids[i], "err", err)
			}
			return nil
		}
	}
	return ids, scheduler.Run(ctx, m.maxConcurrent, wrapped)
}
