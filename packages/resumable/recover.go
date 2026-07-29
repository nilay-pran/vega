// Package resumable restores in-flight uploads after a restart. It contains the
// deterministic recovery step (docs/ARCHITECTURE.md §8.2, §15): whatever state
// SQLite holds is authoritative, so on startup any upload that was mid-transfer
// is made runnable again without losing acked chunks.
package resumable

import (
	"context"

	"code.sli.ke/go/vega/packages/common"
	"code.sli.ke/go/vega/packages/manifests"
)

// Recover prepares every non-terminal upload to resume: chunks left inflight by
// a crash are reset to pending, and the upload is returned to Ready so the
// scheduler will pick it up. Already-acked chunks are untouched, so nothing
// restarts from zero. It returns how many uploads were recovered.
func Recover(ctx context.Context, store *manifests.Store) (int, error) {
	ids, err := store.ListActive(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := store.ResetInFlight(ctx, id); err != nil {
			return 0, err
		}
		if err := store.SetStatus(ctx, id, common.StatusReady, ""); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
