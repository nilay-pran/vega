package transport

import (
	"context"
	"fmt"
	"log/slog"
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

// PathState is a circuit breaker's state for one candidate path.
type PathState int

const (
	// StateClosed is the normal state: the path is in rotation.
	StateClosed PathState = iota
	// StateOpen means the path just failed enough times in a row that it is
	// skipped until Cooldown elapses, so a blocked path is not retried on
	// every single call while it stays blocked.
	StateOpen
	// StateHalfOpen means Cooldown has elapsed and the path is due exactly one
	// trial; a further failure reopens it, a success closes it.
	StateHalfOpen
)

func (s PathState) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// BreakerConfig tunes the circuit breaker shared by every candidate path.
type BreakerConfig struct {
	// FailThreshold is how many consecutive failures (probe or real operation)
	// trip a path from Closed to Open.
	FailThreshold int
	// Cooldown is how long an Open path is skipped before it gets a half-open
	// trial again.
	Cooldown time.Duration
	// ProbeInterval is how often the background monitor re-probes every
	// candidate — the "periodic network path analysis".
	ProbeInterval time.Duration
	// ProbeTimeout bounds a single probe call, so a fully black-holed path
	// (e.g. UDP silently dropped by a firewall, no ICMP reject) fails fast
	// instead of hanging until some outer deadline.
	ProbeTimeout time.Duration
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.FailThreshold < 1 {
		c.FailThreshold = 2
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 30 * time.Second
	}
	if c.ProbeInterval <= 0 {
		c.ProbeInterval = 20 * time.Second
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = 3 * time.Second
	}
	return c
}

// pathHealth is one candidate's breaker state.
type pathHealth struct {
	state       PathState
	consecFails int
	openedAt    time.Time
	lastErr     error
}

// Chooser is a live, continuously health-checked transport selector: a circuit
// breaker per candidate combined with a periodic reachability monitor. Unlike a
// one-shot probe-at-startup pick, a Chooser keeps re-evaluating paths for as
// long as it runs, so a path that goes bad mid-session — or one that
// recovers after a firewall rule changes — shows up on the very next call.
//
// Candidates are given in priority order (fastest/preferred first). Ordered
// returns them ranked by current health; ReportSuccess and ReportFailure let a
// real upload operation feed the same breaker in real time, so a failing
// transfer trips a path immediately rather than waiting for the next probe
// tick — the "realtime circuit breaker" half of the design. Safe for
// concurrent use.
type Chooser struct {
	candidates []Candidate
	cfg        BreakerConfig
	log        *slog.Logger

	mu     sync.Mutex
	health map[string]*pathHealth
}

func NewChooser(log *slog.Logger, cfg BreakerConfig, candidates ...Candidate) *Chooser {
	if log == nil {
		log = slog.Default()
	}
	health := make(map[string]*pathHealth, len(candidates))
	for _, c := range candidates {
		health[c.Name] = &pathHealth{state: StateClosed}
	}
	return &Chooser{candidates: candidates, cfg: cfg.withDefaults(), log: log, health: health}
}

// ProbeNow runs one synchronous probe pass over every candidate and blocks
// until it finishes (bounded by cfg.ProbeTimeout per candidate). Call it once
// at startup so the very first pick reflects real reachability instead of
// every path's default Closed state; Start keeps the same picture current
// afterward.
func (c *Chooser) ProbeNow(ctx context.Context) {
	c.probeAll(ctx)
}

// Start launches the periodic path-analysis monitor: it probes every
// candidate on cfg.ProbeInterval until ctx is done. Call it once per Chooser,
// after an initial ProbeNow. Real-operation feedback via ReportSuccess/
// ReportFailure keeps working even before Start runs or after ctx ends.
func (c *Chooser) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(c.cfg.ProbeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.probeAll(ctx)
			}
		}
	}()
}

func (c *Chooser) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, cand := range c.candidates {
		if cand.Probe == nil {
			c.ReportSuccess(cand.Name)
			continue
		}
		wg.Add(1)
		go func(cand Candidate) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, c.cfg.ProbeTimeout)
			defer cancel()
			if err := cand.Probe(pctx); err != nil {
				c.ReportFailure(cand.Name, err)
			} else {
				c.ReportSuccess(cand.Name)
			}
		}(cand)
	}
	wg.Wait()
}

// Ordered ranks the candidates by current health: every path that is not
// presently tripped (Closed or Cooldown-elapsed Open, which is promoted to a
// half-open trial here) comes first, in priority order; still-cooling-down
// Open paths follow, so a caller always has something to try — the upload
// never simply refuses to attempt a transfer, even when every path currently
// looks bad.
func (c *Chooser) Ordered() []Candidate {
	c.mu.Lock()
	defer c.mu.Unlock()
	ready := make([]Candidate, 0, len(c.candidates))
	cooling := make([]Candidate, 0)
	for _, cand := range c.candidates {
		h := c.health[cand.Name]
		if h.state != StateOpen || time.Since(h.openedAt) >= c.cfg.Cooldown {
			if h.state == StateOpen {
				h.state = StateHalfOpen
			}
			ready = append(ready, cand)
		} else {
			cooling = append(cooling, cand)
		}
	}
	return append(ready, cooling...)
}

// ReportSuccess records a successful call (probe or real operation) against
// the named path, closing its breaker.
func (c *Chooser) ReportSuccess(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.health[name]
	if !ok {
		return
	}
	if h.state != StateClosed {
		c.log.Info("transport path recovered", "path", name)
	}
	h.state = StateClosed
	h.consecFails = 0
	h.lastErr = nil
}

// ReportFailure records a failed call against the named path. FailThreshold
// consecutive failures trip it open; a failure while already open or
// half-open restarts the cooldown.
func (c *Chooser) ReportFailure(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.health[name]
	if !ok {
		return
	}
	h.lastErr = err
	if h.state == StateClosed {
		h.consecFails++
		if h.consecFails < c.cfg.FailThreshold {
			return
		}
	}
	wasOpen := h.state == StateOpen
	h.state = StateOpen
	h.openedAt = time.Now()
	if !wasOpen {
		c.log.Warn("transport path tripped", "path", name, "err", err, "consec_fails", h.consecFails)
	}
}

// PathStatus reports one candidate's current breaker state, for logging or
// future diagnostics surfacing.
type PathStatus struct {
	Name    string
	State   PathState
	LastErr string
}

// Snapshot reports every candidate's current breaker state.
func (c *Chooser) Snapshot() []PathStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]PathStatus, len(c.candidates))
	for i, cand := range c.candidates {
		h := c.health[cand.Name]
		s := PathStatus{Name: cand.Name, State: h.state}
		if h.lastErr != nil {
			s.LastErr = h.lastErr.Error()
		}
		out[i] = s
	}
	return out
}

// errNoCandidates is returned when a Chooser has nothing configured at all —
// a programming error (every call site passes at least one candidate), not a
// reachability failure.
var errNoCandidates = fmt.Errorf("transport: no candidates configured")
