package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMetrics_Snapshot(t *testing.T) {
	m := New()
	m.L1Hits.Add(7)
	m.L1Misses.Add(3)
	m.L2Hits.Add(2)
	m.L3Hits.Add(1)
	m.ObserveLatency(100 * time.Microsecond)
	m.ObserveLatency(300 * time.Microsecond)

	s := m.Snapshot()
	assert.Equal(t, uint64(7), s.L1Hits)
	assert.Equal(t, uint64(2), s.Requests)
	assert.InDelta(t, 200.0, s.AvgLatencyMicros, 0.001)

	// Overall served = L1+L2+L3 hits = 10; lookups = L1Hits+L1Misses = 10.
	assert.InDelta(t, 1.0, s.OverallHitRatio, 0.001)
}

func TestMetrics_EmptySnapshot(t *testing.T) {
	s := New().Snapshot()
	assert.Zero(t, s.AvgLatencyMicros)
	assert.Zero(t, s.OverallHitRatio)
}
