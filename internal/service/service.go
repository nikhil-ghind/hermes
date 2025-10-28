// Package service implements the core Hermes caching logic: a multi-level
// read-through / write-through cache spanning three tiers:
//
//	L1 — in-process LRU (cache.LRU): nanosecond access, per-instance.
//	L2 — Redis (cache.L2Cache): shared across instances, millisecond access.
//	L3 — Cassandra (store.Store): durable system of record.
//
// Reads fall through L1 -> L2 -> L3, back-filling the faster tiers on a hit
// deeper down. Writes and deletes propagate through every tier (write-through)
// so the caches never serve data that is staler than the backing store. A
// request coalescer collapses concurrent misses for the same key into a single
// backing fetch to prevent cache stampedes.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/nikhil-ghind/hermes/internal/cache"
	"github.com/nikhil-ghind/hermes/internal/metrics"
	"github.com/nikhil-ghind/hermes/internal/store"
)

// ErrNotFound indicates the key was absent in every tier.
var ErrNotFound = errors.New("service: key not found")

// Service ties the three cache tiers together. It is safe for concurrent use by
// many goroutines.
type Service struct {
	l1        *cache.LRU
	l2        cache.L2Cache
	l3        store.Store
	coalescer *Coalescer
	metrics   *metrics.Metrics
	redisTTL  time.Duration
}

// Options configures a Service.
type Options struct {
	L1       *cache.LRU
	L2       cache.L2Cache
	L3       store.Store
	Metrics  *metrics.Metrics
	RedisTTL time.Duration
}

// New constructs a Service. Metrics is created if nil.
func New(opts Options) *Service {
	m := opts.Metrics
	if m == nil {
		m = metrics.New()
	}
	return &Service{
		l1:        opts.L1,
		l2:        opts.L2,
		l3:        opts.L3,
		coalescer: NewCoalescer(),
		metrics:   m,
		redisTTL:  opts.RedisTTL,
	}
}

// Metrics returns the underlying metrics registry for snapshotting.
func (s *Service) Metrics() *metrics.Metrics {
	return s.metrics
}

// Get implements the read-through path.
//
//  1. Check L1 (LRU). On hit, return immediately.
//  2. On L1 miss, coalesce concurrent callers for the same key and perform a
//     single backing lookup that checks L2 (Redis) then L3 (Cassandra),
//     back-filling the faster tiers as it goes.
func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	start := time.Now()
	defer func() { s.metrics.ObserveLatency(time.Since(start)) }()

	// --- L1 ---
	if v, ok := s.l1.Get(key); ok {
		s.metrics.L1Hits.Add(1)
		return v, nil
	}
	s.metrics.L1Misses.Add(1)

	// --- L2/L3 via coalescer ---
	val, err, shared := s.coalescer.Do(key, func() ([]byte, error) {
		return s.fetchThroughL2L3(ctx, key)
	})
	if shared {
		s.metrics.CoalescedRequests.Add(1)
	}
	if err != nil {
		return nil, err
	}
	return val, nil
}

// fetchThroughL2L3 is executed by exactly one goroutine per key (via the
// coalescer). It checks Redis, then Cassandra, back-filling on the way up.
func (s *Service) fetchThroughL2L3(ctx context.Context, key string) ([]byte, error) {
	// --- L2 (Redis) ---
	v, err := s.l2.Get(ctx, key)
	switch {
	case err == nil:
		s.metrics.L2Hits.Add(1)
		// Back-fill L1.
		s.l1.Put(key, v)
		return v, nil
	case errors.Is(err, cache.ErrNotFound):
		s.metrics.L2Misses.Add(1)
		// fall through to L3
	default:
		s.metrics.Errors.Add(1)
		return nil, err
	}

	// --- L3 (Cassandra) ---
	v, err = s.l3.Get(ctx, key)
	switch {
	case err == nil:
		s.metrics.L3Hits.Add(1)
		// Back-fill L2 then L1. A failure to back-fill Redis is non-fatal: the
		// read still succeeds, we just lose the caching benefit for this key.
		if setErr := s.l2.Set(ctx, key, v, s.redisTTL); setErr != nil {
			s.metrics.Errors.Add(1)
		}
		s.l1.Put(key, v)
		return v, nil
	case errors.Is(err, store.ErrNotFound):
		s.metrics.L3Misses.Add(1)
		return nil, ErrNotFound
	default:
		s.metrics.Errors.Add(1)
		return nil, err
	}
}

// Put implements the write-through path: the durable store (L3) is written
// first so it is always authoritative, then the caches are refreshed (L2) and
// invalidated/updated (L1). Writing the new value into the caches keeps reads
// warm; alternatively L1 could be invalidated, but updating avoids a guaranteed
// subsequent miss.
func (s *Service) Put(ctx context.Context, key string, value []byte) error {
	start := time.Now()
	defer func() { s.metrics.ObserveLatency(time.Since(start)) }()

	// 1. Durable write first (system of record).
	if err := s.l3.Put(ctx, key, value); err != nil {
		s.metrics.Errors.Add(1)
		return err
	}

	// 2. Refresh L2. A Redis failure here is non-fatal for correctness because
	//    the next read will fall through to L3; but we surface it via metrics.
	if err := s.l2.Set(ctx, key, value, s.redisTTL); err != nil {
		s.metrics.Errors.Add(1)
	}

	// 3. Update L1 so this instance serves the fresh value immediately.
	s.l1.Put(key, value)

	s.metrics.Writes.Add(1)
	return nil
}

// Delete implements write-through deletion: remove from the durable store, then
// invalidate both cache tiers so no stale value is served.
func (s *Service) Delete(ctx context.Context, key string) error {
	start := time.Now()
	defer func() { s.metrics.ObserveLatency(time.Since(start)) }()

	if err := s.l3.Delete(ctx, key); err != nil {
		s.metrics.Errors.Add(1)
		return err
	}
	if err := s.l2.Delete(ctx, key); err != nil {
		s.metrics.Errors.Add(1)
	}
	s.l1.Delete(key)

	s.metrics.Deletes.Add(1)
	return nil
}
