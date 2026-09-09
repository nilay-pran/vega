package transport

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func testLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestChooserOrderedPrefersReachablePrimary(t *testing.T) {
	ok := func(context.Context) error { return nil }
	c := NewChooser(testLog(), BreakerConfig{}, Candidate{Name: "slkt", Probe: ok}, Candidate{Name: "http", Probe: ok})
	c.ProbeNow(context.Background())
	if got := c.Ordered()[0].Name; got != "slkt" {
		t.Fatalf("got %q, want slkt", got)
	}
}

func TestChooserFallsBackWhenPrimaryBlocked(t *testing.T) {
	blocked := func(context.Context) error { return errors.New("blocked") }
	ok := func(context.Context) error { return nil }
	c := NewChooser(testLog(), BreakerConfig{FailThreshold: 1}, Candidate{Name: "slkt", Probe: blocked}, Candidate{Name: "http", Probe: ok})
	c.ProbeNow(context.Background())
	if got := c.Ordered()[0].Name; got != "http" {
		t.Fatalf("got %q, want http", got)
	}
}

// A path that keeps failing trips open and is skipped even though it "answers"
// nil-probe candidates immediately; once its cooldown elapses it is offered
// again as a half-open trial.
func TestChooserOpensAndHalfOpensAfterCooldown(t *testing.T) {
	fails := 0
	flaky := func(context.Context) error {
		fails++
		return errors.New("down")
	}
	ok := func(context.Context) error { return nil }
	cfg := BreakerConfig{FailThreshold: 2, Cooldown: 20 * time.Millisecond, ProbeTimeout: time.Second}
	c := NewChooser(testLog(), cfg, Candidate{Name: "primary", Probe: flaky}, Candidate{Name: "fallback", Probe: ok})

	c.ProbeNow(context.Background())
	c.ProbeNow(context.Background()) // second consecutive failure trips it open
	if got := c.Ordered()[0].Name; got != "fallback" {
		t.Fatalf("after tripping, got %q, want fallback first", got)
	}

	time.Sleep(30 * time.Millisecond) // let the cooldown elapse
	names := make([]string, 0, 2)
	for _, cand := range c.Ordered() {
		names = append(names, cand.Name)
	}
	if names[0] != "primary" {
		t.Fatalf("after cooldown, got order %v, want primary offered as half-open trial first", names)
	}
}

// ReportFailure/ReportSuccess let a real upload operation trip or heal a path
// without waiting for the next periodic probe tick — the realtime half of the
// circuit breaker.
func TestChooserRealtimeReportFeedsTheSameBreaker(t *testing.T) {
	c := NewChooser(testLog(), BreakerConfig{FailThreshold: 1}, Candidate{Name: "a"}, Candidate{Name: "b"})
	if got := c.Ordered()[0].Name; got != "a" {
		t.Fatalf("got %q, want a", got)
	}
	c.ReportFailure("a", errors.New("connection reset"))
	if got := c.Ordered()[0].Name; got != "b" {
		t.Fatalf("after realtime failure, got %q, want b", got)
	}
	c.ReportSuccess("a")
	if got := c.Ordered()[0].Name; got != "a" {
		t.Fatalf("after realtime success, got %q, want a restored to first", got)
	}
}

func TestChooserSnapshotReportsState(t *testing.T) {
	c := NewChooser(testLog(), BreakerConfig{FailThreshold: 1}, Candidate{Name: "a"}, Candidate{Name: "b"})
	c.ReportFailure("a", errors.New("boom"))
	snap := c.Snapshot()
	if len(snap) != 2 || snap[0].Name != "a" || snap[0].State != StateOpen || snap[0].LastErr == "" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap[1].State != StateClosed {
		t.Fatalf("unexpected snapshot for b: %+v", snap[1])
	}
}
