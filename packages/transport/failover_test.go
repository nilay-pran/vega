package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSelect(t *testing.T) {
	ok := func(context.Context) error { return nil }
	blocked := func(context.Context) error { return errors.New("blocked") }
	ctx := context.Background()

	t.Run("prefers reachable primary", func(t *testing.T) {
		c, err := Select(ctx, time.Second,
			Candidate{Name: "slkt", Probe: ok},
			Candidate{Name: "http", Probe: ok})
		if err != nil || c.Name != "slkt" {
			t.Fatalf("got %q, %v; want slkt", c.Name, err)
		}
	})

	t.Run("falls back when primary blocked", func(t *testing.T) {
		c, err := Select(ctx, time.Second,
			Candidate{Name: "slkt", Probe: blocked},
			Candidate{Name: "http", Probe: ok})
		if err != nil || c.Name != "http" {
			t.Fatalf("got %q, %v; want http", c.Name, err)
		}
	})

	t.Run("errors when none reachable", func(t *testing.T) {
		if _, err := Select(ctx, time.Second,
			Candidate{Name: "slkt", Probe: blocked},
			Candidate{Name: "http", Probe: blocked}); err == nil {
			t.Fatal("expected error when no transport reachable")
		}
	})
}
