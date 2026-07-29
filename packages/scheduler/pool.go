// Package scheduler runs chunk work with bounded concurrency. It is deliberately
// tiny for v1: a fixed-limit worker pool. Adaptive parallelism (deriving the
// limit from the live bandwidth-delay product, docs/ARCHITECTURE.md §9) plugs in
// here later without changing callers.
package scheduler

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// Task is one unit of work (typically "upload one chunk").
type Task func(ctx context.Context) error

// Run executes tasks with at most `limit` running concurrently. It returns the
// first error and cancels the shared context so the rest stop promptly.
func Run(ctx context.Context, limit int, tasks []Task) error {
	if limit < 1 {
		limit = 1
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for _, t := range tasks {
		g.Go(func() error { return t(ctx) })
	}
	return g.Wait()
}
