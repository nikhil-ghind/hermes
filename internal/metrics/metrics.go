// Package metrics provides lightweight, concurrency-safe counters for the
// Hermes caching service. All counters use sync/atomic so they can be updated
// from any goroutine on the hot path without locking.
package metrics

import (
	"sync/atomic"
	"time"
)

// Metrics holds atomic counters describing cache behaviour across the three
// cache layers (L1 in-process LRU, L2 Redis, L3 Cassandra) plus aggregate
// latency accounting.
type Metrics struct {
	// Per-layer hit/miss counters.
	L1Hits   atomic.Uint64
	L1Misses atomic.Uint64
	L2Hits   atomic.Uint64
	L2Misses atomic.Uint64
	L3Hits   atomic.Uint64
	L3Misses atomic.Uint64

	// Write-through and delete accounting.
	Writes  atomic.Uint64
	Deletes atomic.Uint64

	// Request coalescing: how many callers were served by a shared backing
	// fetch instead of issuing their own.
	CoalescedRequests atomic.Uint64

	// Errors observed while talking to a backing layer.
	Errors atomic.Uint64

	// Aggregate request latency accounting (nanoseconds) used to derive a
	// running average without storing every sample.
	totalLatencyNanos atomic.Uint64
	requestCount      atomic.Uint64
}

// New returns a zeroed Metrics instance ready for use.
func New() *Metrics {
	return &Metrics{}
}

// ObserveLatency records the duration of a single request for averaging.
func (m *Metrics) ObserveLatency(d time.Duration) {
	m.totalLatencyNanos.Add(uint64(d.Nanoseconds()))
	m.requestCount.Add(1)
}

// Snapshot is an immutable, point-in-time view of the metrics suitable for
// JSON serialisation on the /metrics endpoint.
type Snapshot struct {
	L1Hits            uint64  `json:"l1_hits"`
	L1Misses          uint64  `json:"l1_misses"`
	L2Hits            uint64  `json:"l2_hits"`
	L2Misses          uint64  `json:"l2_misses"`
	L3Hits            uint64  `json:"l3_hits"`
	L3Misses          uint64  `json:"l3_misses"`
	Writes            uint64  `json:"writes"`
	Deletes           uint64  `json:"deletes"`
	CoalescedRequests uint64  `json:"coalesced_requests"`
	Errors            uint64  `json:"errors"`
	Requests          uint64  `json:"requests"`
	AvgLatencyMicros  float64 `json:"avg_latency_micros"`
	OverallHitRatio   float64 `json:"overall_hit_ratio"`
}

// Snapshot atomically reads all counters and computes derived statistics.
func (m *Metrics) Snapshot() Snapshot {
	l1h := m.L1Hits.Load()
	l2h := m.L2Hits.Load()
	l3h := m.L3Hits.Load()
	l1m := m.L1Misses.Load()
	reqs := m.requestCount.Load()

	var avg float64
	if reqs > 0 {
		avg = float64(m.totalLatencyNanos.Load()) / float64(reqs) / 1000.0
	}

	// Overall hit ratio is measured against top-of-funnel lookups, i.e. how
	// often any cached layer (L1, L2 or L3) satisfied a request that entered
	// at L1. l1Misses approximates the number of lookups that fell through L1.
	var ratio float64
	totalServed := l1h + l2h + l3h
	totalLookups := l1h + l1m
	if totalLookups > 0 {
		ratio = float64(totalServed) / float64(totalLookups)
	}

	return Snapshot{
		L1Hits:            l1h,
		L1Misses:          l1m,
		L2Hits:            l2h,
		L2Misses:          m.L2Misses.Load(),
		L3Hits:            l3h,
		L3Misses:          m.L3Misses.Load(),
		Writes:            m.Writes.Load(),
		Deletes:           m.Deletes.Load(),
		CoalescedRequests: m.CoalescedRequests.Load(),
		Errors:            m.Errors.Load(),
		Requests:          reqs,
		AvgLatencyMicros:  avg,
		OverallHitRatio:   ratio,
	}
}
