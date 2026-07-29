package telemetry

import (
	"testing"
	"time"
)

func TestSamplerRatesAndETA(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := NewSampler(0.5)

	// 10 MB at t=0, then +10 MB each second for 3 seconds → 10 MB/s.
	s.Observe(0, base)
	for i := 1; i <= 3; i++ {
		s.Observe(int64(i)*10<<20, base.Add(time.Duration(i)*time.Second))
	}

	const mb = float64(1 << 20)
	if cur := s.CurrentBPS() / mb; cur < 9.9 || cur > 10.1 {
		t.Fatalf("current = %.2f MB/s, want ~10", cur)
	}
	if avg := s.AverageBPS() / mb; avg < 9.9 || avg > 10.1 {
		t.Fatalf("average = %.2f MB/s, want ~10", avg)
	}
	// 30 MB remaining at 10 MB/s → ~3s.
	if eta := s.ETA(30 << 20); eta < 2900*time.Millisecond || eta > 3100*time.Millisecond {
		t.Fatalf("eta = %s, want ~3s", eta)
	}
}

func TestSamplerZeroBeforeData(t *testing.T) {
	s := NewSampler(0.3)
	if s.CurrentBPS() != 0 || s.AverageBPS() != 0 || s.ETA(100) != 0 {
		t.Fatal("expected zeroes before any observation")
	}
}

func TestMetrics(t *testing.T) {
	m := NewMetrics()
	m.Inc("uploads")
	m.Add("bytes", 500)
	m.Add("bytes", 500)
	snap := m.Snapshot()
	if snap["uploads"] != 1 || snap["bytes"] != 1000 {
		t.Fatalf("snapshot = %+v", snap)
	}
}
