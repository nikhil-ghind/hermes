package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikhil-ghind/hermes/internal/cache"
	"github.com/nikhil-ghind/hermes/internal/store"
)

// --- fakes ---

// fakeL2 is an in-memory implementation of cache.L2Cache for tests. It records
// call counts so tests can assert on back-fill behaviour.
type fakeL2 struct {
	mu       sync.Mutex
	data     map[string][]byte
	getCalls atomic.Int64
	setCalls atomic.Int64
	delCalls atomic.Int64
}

func newFakeL2() *fakeL2 { return &fakeL2{data: make(map[string][]byte)} }

func (f *fakeL2) Get(_ context.Context, key string) ([]byte, error) {
	f.getCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (f *fakeL2) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.setCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	return nil
}

func (f *fakeL2) Delete(_ context.Context, key string) error {
	f.delCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeL2) Ping(_ context.Context) error { return nil }
func (f *fakeL2) Close() error                 { return nil }

// fakeL3 is an in-memory implementation of store.Store for tests.
type fakeL3 struct {
	mu       sync.Mutex
	data     map[string][]byte
	getCalls atomic.Int64
	putCalls atomic.Int64
	delCalls atomic.Int64
	// gate, if non-nil, blocks every Get until closed (used to force coalescing).
	gate chan struct{}
}

func newFakeL3() *fakeL3 { return &fakeL3{data: make(map[string][]byte)} }

func (f *fakeL3) Get(_ context.Context, key string) ([]byte, error) {
	f.getCalls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (f *fakeL3) Put(_ context.Context, key string, value []byte) error {
	f.putCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	return nil
}

func (f *fakeL3) Delete(_ context.Context, key string) error {
	f.delCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeL3) Close() {}

// newTestService builds a Service backed by the supplied fakes.
func newTestService(l2 *fakeL2, l3 *fakeL3) *Service {
	return New(Options{
		L1:       cache.NewLRU(64, 0),
		L2:       l2,
		L3:       l3,
		RedisTTL: time.Minute,
	})
}

// --- tests ---

func TestService_Get_L3HitBackfillsL2AndL1(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	require.NoError(t, l3.Put(context.Background(), "k", []byte("v")))
	svc := newTestService(l2, l3)

	// First read: L1 miss, L2 miss, L3 hit -> back-fill L1 and L2.
	v, err := svc.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), v)

	assert.Equal(t, int64(1), l3.getCalls.Load(), "L3 hit on first read")
	assert.Equal(t, int64(1), l2.setCalls.Load(), "L2 back-filled")

	// Second read should be served by L1: no new L2 or L3 traffic.
	v, err = svc.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), v)
	assert.Equal(t, int64(1), l3.getCalls.Load(), "no extra L3 read")

	snap := svc.Metrics().Snapshot()
	assert.Equal(t, uint64(1), snap.L1Hits)
	assert.Equal(t, uint64(1), snap.L3Hits)
}

func TestService_Get_L2HitBackfillsL1Only(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	require.NoError(t, l2.Set(context.Background(), "k", []byte("v"), time.Minute))
	l2.setCalls.Store(0) // reset after seeding
	svc := newTestService(l2, l3)

	v, err := svc.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), v)

	assert.Equal(t, int64(0), l3.getCalls.Load(), "L2 hit must not touch L3")
	assert.Equal(t, int64(0), l2.setCalls.Load(), "L2 hit does not re-set L2")

	snap := svc.Metrics().Snapshot()
	assert.Equal(t, uint64(1), snap.L2Hits)
}

func TestService_Get_NotFound(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	svc := newTestService(l2, l3)

	_, err := svc.Get(context.Background(), "absent")
	assert.ErrorIs(t, err, ErrNotFound)

	snap := svc.Metrics().Snapshot()
	assert.Equal(t, uint64(1), snap.L3Misses)
}

func TestService_Put_WriteThrough(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	svc := newTestService(l2, l3)

	require.NoError(t, svc.Put(context.Background(), "k", []byte("v")))

	assert.Equal(t, int64(1), l3.putCalls.Load(), "durable store written")
	assert.Equal(t, int64(1), l2.setCalls.Load(), "L2 refreshed")

	// Subsequent Get should be a pure L1 hit (no backing traffic).
	v, err := svc.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), v)
	assert.Equal(t, int64(0), l3.getCalls.Load())

	snap := svc.Metrics().Snapshot()
	assert.Equal(t, uint64(1), snap.Writes)
	assert.Equal(t, uint64(1), snap.L1Hits)
}

func TestService_Delete_InvalidatesAllTiers(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	svc := newTestService(l2, l3)

	require.NoError(t, svc.Put(context.Background(), "k", []byte("v")))
	require.NoError(t, svc.Delete(context.Background(), "k"))

	assert.Equal(t, int64(1), l3.delCalls.Load())
	assert.Equal(t, int64(1), l2.delCalls.Load())

	_, err := svc.Get(context.Background(), "k")
	assert.ErrorIs(t, err, ErrNotFound, "deleted key must not be served from any tier")

	snap := svc.Metrics().Snapshot()
	assert.Equal(t, uint64(1), snap.Deletes)
}

func TestService_Get_CoalescesConcurrentMisses(t *testing.T) {
	l2, l3 := newFakeL2(), newFakeL3()
	require.NoError(t, l3.Put(context.Background(), "hot", []byte("v")))
	l3.gate = make(chan struct{})
	svc := newTestService(l2, l3)

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := svc.Get(context.Background(), "hot")
			assert.NoError(t, err)
			assert.Equal(t, []byte("v"), v)
		}()
	}

	// Let the goroutines coalesce on the single in-flight backing fetch.
	assert.Eventually(t, func() bool { return l3.getCalls.Load() >= 1 }, time.Second, time.Millisecond)
	close(l3.gate) // release the single backing fetch
	wg.Wait()

	assert.Equal(t, int64(1), l3.getCalls.Load(), "concurrent misses must collapse to one L3 read")

	snap := svc.Metrics().Snapshot()
	assert.Positive(t, snap.CoalescedRequests, "coalesced requests should be counted")
}
