// Package telemetry measures transfer performance: a bandwidth sampler that
// drives the live speed/ETA shown in the UI and persisted to
// transfer_statistics, plus a small metrics registry for diagnostics
// (docs/ARCHITECTURE.md §16, Observability). It holds no I/O; callers supply
// timestamps so it is deterministic and trivially testable.
package telemetry

import "time"

// Sampler tracks throughput from a stream of cumulative-bytes observations. It
// exposes a smoothed current rate (EWMA, responsive to recent conditions) and a
// running average, and derives an ETA. Not safe for concurrent use; guard it in
// the caller (the engine holds one per upload behind a mutex).
type Sampler struct {
	alpha     float64
	ewma      float64
	haveEWMA  bool
	started   bool
	start     time.Time
	last      time.Time
	lastBytes int64
	total     int64
}

// NewSampler builds a sampler. alpha in (0,1] weights the most recent rate; 0.3
// is a reasonable default (responsive but not jittery).
func NewSampler(alpha float64) *Sampler {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	return &Sampler{alpha: alpha}
}

// Observe records that cumulativeBytes have been transferred as of time at.
func (s *Sampler) Observe(cumulativeBytes int64, at time.Time) {
	s.total = cumulativeBytes
	if !s.started {
		s.started, s.start, s.last, s.lastBytes = true, at, at, cumulativeBytes
		return
	}
	dt := at.Sub(s.last).Seconds()
	if dt <= 0 {
		return
	}
	rate := float64(cumulativeBytes-s.lastBytes) / dt
	if s.haveEWMA {
		s.ewma = s.alpha*rate + (1-s.alpha)*s.ewma
	} else {
		s.ewma, s.haveEWMA = rate, true
	}
	s.last, s.lastBytes = at, cumulativeBytes
}

// CurrentBPS is the smoothed recent rate in bytes/sec.
func (s *Sampler) CurrentBPS() float64 { return s.ewma }

// AverageBPS is total bytes over total elapsed time.
func (s *Sampler) AverageBPS() float64 {
	if !s.started {
		return 0
	}
	elapsed := s.last.Sub(s.start).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(s.total) / elapsed
}

// ETA estimates time to transfer remaining bytes at the current rate.
func (s *Sampler) ETA(remaining int64) time.Duration {
	rate := s.ewma
	if rate <= 0 {
		rate = s.AverageBPS()
	}
	if rate <= 0 || remaining <= 0 {
		return 0
	}
	return time.Duration(float64(remaining) / rate * float64(time.Second))
}
