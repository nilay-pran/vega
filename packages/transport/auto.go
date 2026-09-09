package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"

	"code.sli.ke/go/vega/packages/storage"
	"code.sli.ke/go/vega/packages/transport/slkt"
)

// AutoConfig holds what every entry point (uploaderd, uploader-cli) needs to
// resolve a -transport flag against one upload server. It is the single place
// that shape is defined, so the waterfall and its resilience wiring have one
// implementation instead of one per binary.
type AutoConfig struct {
	Server   string
	Token    string
	Insecure bool
	// Breaker tunes the circuit breaker "auto" mode runs on top of. Zero value
	// gets sensible defaults (see BreakerConfig.withDefaults).
	Breaker BreakerConfig
}

func (c AutoConfig) httpClient() *http.Client {
	hc := &http.Client{}
	if c.Insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return hc
}

// candidates builds the three standard waterfall candidates against the same
// server URL: QUIC/HTTP-3 primary, HTTPS-over-TCP failover, SLKT trailing
// while it is still evaluated against H3 (docs/ARCHITECTURE.md §7).
func (c AutoConfig) candidates() ([]Candidate, error) {
	slktAddr, err := SLKTAddr(c.Server)
	if err != nil {
		return nil, err
	}
	httpStore := NewHTTPStore(c.Server, c.Token, c.httpClient())
	h3Store := NewHTTP3Store(c.Server, c.Token, c.Insecure)
	slktClient := slkt.NewClient(slktAddr)
	return []Candidate{
		{Name: "h3", Store: h3Store, Probe: h3Store.Probe},
		{Name: "http", Store: httpStore, Probe: httpStore.Probe},
		{Name: "slkt", Store: slktClient, Probe: slktClient.Probe},
	}, nil
}

// BuildAuto runs the full waterfall setup for "auto" mode: it builds the
// standard candidates, probes them once synchronously so startup logs which
// path is live (matching the old one-shot Select's UX), then starts the live
// Chooser for the rest of ctx's lifetime — the periodic network-path analysis
// — and returns a FailoverStore, the realtime circuit-breaker failover, in
// front of it. ctx should be the caller's long-lived context: the daemon's
// run context, or a CLI invocation's own.
func BuildAuto(ctx context.Context, cfg AutoConfig, log *slog.Logger) (storage.ObjectStore, error) {
	candidates, err := cfg.candidates()
	if err != nil {
		return nil, err
	}
	chooser := NewChooser(log, cfg.Breaker, candidates...)
	chooser.ProbeNow(ctx)
	log.Info("transport selected", "name", chooser.Ordered()[0].Name)
	chooser.Start(ctx)
	return NewFailoverStore(chooser, candidates...), nil
}

// Choose resolves one of the named transport modes into a concrete
// ObjectStore: slkt | h3 | http | presigned | auto. It is the single place
// both uploaderd and uploader-cli turn a -transport flag into a store, so a
// resilience fix made here reaches every entry point at once.
func Choose(ctx context.Context, mode string, cfg AutoConfig, log *slog.Logger) (storage.ObjectStore, error) {
	switch mode {
	case "slkt":
		slktAddr, err := SLKTAddr(cfg.Server)
		if err != nil {
			return nil, err
		}
		return slkt.NewClient(slktAddr), nil
	case "h3":
		return NewHTTP3Store(cfg.Server, cfg.Token, cfg.Insecure), nil
	case "http":
		return NewHTTPStore(cfg.Server, cfg.Token, cfg.httpClient()), nil
	case "presigned":
		// Control plane over HTTPS; part bytes go client→Spaces directly.
		// Requires the server to run an S3/Spaces backend (it registers the
		// presign route).
		return NewPresignedStore(cfg.Server, cfg.Token, cfg.httpClient(), cfg.Insecure), nil
	case "auto":
		return BuildAuto(ctx, cfg, log)
	default:
		return nil, fmt.Errorf("unknown transport %q (want auto, h3, slkt, http, or presigned)", mode)
	}
}
