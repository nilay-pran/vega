package telemetry

import "sync"

// Metrics is a minimal concurrent counter registry for diagnostics (chunks
// uploaded, bytes sent, retries, uploads completed). It is intentionally small;
// a Prometheus/OTel exporter can read Snapshot without changing call sites.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]int64
}

func NewMetrics() *Metrics { return &Metrics{counters: make(map[string]int64)} }

func (m *Metrics) Add(name string, delta int64) {
	m.mu.Lock()
	m.counters[name] += delta
	m.mu.Unlock()
}

func (m *Metrics) Inc(name string) { m.Add(name, 1) }

// Snapshot returns a copy of all counters.
func (m *Metrics) Snapshot() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.counters))
	for k, v := range m.counters {
		out[k] = v
	}
	return out
}
