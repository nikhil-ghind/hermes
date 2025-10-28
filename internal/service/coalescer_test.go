package service

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoalescer_SingleCallReturnsValue(t *testing.T) {
	c := NewCoalescer()
	val, err, shared := c.Do("k", func() ([]byte, error) {
		return []byte("v"), nil
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), val)
	assert.False(t, shared, "a lone caller is not shared")
	assert.Equal(t, 0, c.InFlight(), "call should be cleaned up")
}

func TestCoalescer_ConcurrentCallersShareOneFetch(t *testing.T) {
	c := NewCoalescer()

	var calls atomic.Int64
	release := make(chan struct{})

	fn := func() ([]byte, error) {
		calls.Add(1)
		<-release // block so all callers pile up on the same in-flight call
		return []byte("shared"), nil
	}

	const n = 50
	var wg sync.WaitGroup
	results := make([][]byte, n)
	sharedFlags := make([]bool, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, err, shared := c.Do("hot-key", fn)
			require.NoError(t, err)
			results[idx] = v
			sharedFlags[idx] = shared
		}(i)
	}

	// Give goroutines time to coalesce, then release the single fetch.
	require.Eventually(t, func() bool { return c.InFlight() == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int64(1), calls.Load(), "fn must execute exactly once for the shared key")

	sharedCount := 0
	for i := 0; i < n; i++ {
		assert.Equal(t, []byte("shared"), results[i])
		if sharedFlags[i] {
			sharedCount++
		}
	}
	assert.Positive(t, sharedCount, "at least some callers should be marked as shared")
	assert.Equal(t, 0, c.InFlight())
}

func TestCoalescer_DistinctKeysRunIndependently(t *testing.T) {
	c := NewCoalescer()
	var calls atomic.Int64

	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := strconv.Itoa(idx)
			v, err, _ := c.Do(key, func() ([]byte, error) {
				calls.Add(1)
				return []byte(key), nil
			})
			require.NoError(t, err)
			assert.Equal(t, []byte(key), v)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int64(n), calls.Load(), "each distinct key triggers its own fetch")
}

func TestCoalescer_SubsequentCallRefetches(t *testing.T) {
	c := NewCoalescer()
	var calls atomic.Int64

	for i := 0; i < 3; i++ {
		_, _, _ = c.Do("k", func() ([]byte, error) {
			calls.Add(1)
			return []byte("v"), nil
		})
	}
	// Sequential (non-overlapping) calls are not coalesced.
	assert.Equal(t, int64(3), calls.Load())
}
