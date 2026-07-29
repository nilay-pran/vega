package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"code.sli.ke/go/vega/packages/storage"
)

// Candidate is one transport the chooser may select: a named ObjectStore plus a
// reachability probe. Probe may be nil to mean "always reachable".
type Candidate struct {
	Name  string
	Store storage.ObjectStore
	Probe func(context.Context) error
}

// Select realizes the hybrid, protocol-primary + failover decision
// (docs/ARCHITECTURE.md §0, §7): it probes every candidate concurrently within
// timeout and returns the earliest-listed reachable one. List the custom
// protocol first and the direct/HTTP fallback next; when a firewall blocks the
// custom port the primary probe fails and the fallback is chosen. It returns an
// error only if none are reachable.
//
// Selection is per-upload: the chosen transport carries the whole upload. Mid
// upload cross-transport switching needs shared server-side session state and is
// deferred to the horizontally-scalable server (P7).
func Select(ctx context.Context, timeout time.Duration, candidates ...Candidate) (Candidate, error) {
	if len(candidates) == 0 {
		return Candidate{}, fmt.Errorf("transport: no candidates")
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reachable := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		if c.Probe == nil {
			reachable[i] = true
			continue
		}
		wg.Add(1)
		go func(i int, probe func(context.Context) error) {
			defer wg.Done()
			reachable[i] = probe(pctx) == nil
		}(i, c.Probe)
	}
	wg.Wait()

	for i, ok := range reachable {
		if ok {
			return candidates[i], nil
		}
	}
	return Candidate{}, fmt.Errorf("transport: no reachable candidate among %d", len(candidates))
}
