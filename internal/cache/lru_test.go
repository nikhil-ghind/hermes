package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLRU_GetPut(t *testing.T) {
	c := NewLRU(2, 0)

	c.Put("a", []byte("1"))
	c.Put("b", []byte("2"))

	v, ok := c.Get("a")
	require.True(t, ok)
	assert.Equal(t, []byte("1"), v)

	v, ok = c.Get("b")
	require.True(t, ok)
	assert.Equal(t, []byte("2"), v)

	_, ok = c.Get("missing")
	assert.False(t, ok)
}

func TestLRU_Eviction(t *testing.T) {
	tests := []struct {
		name       string
		ops        func(c *LRU)
		wantKeep   []string
		wantEvicts []string
	}{
		{
			name: "evicts least recently used",
			ops: func(c *LRU) {
				c.Put("a", []byte("1"))
				c.Put("b", []byte("2"))
				c.Put("c", []byte("3")) // evicts a
			},
			wantKeep:   []string{"b", "c"},
			wantEvicts: []string{"a"},
		},
		{
			name: "get promotes recency",
			ops: func(c *LRU) {
				c.Put("a", []byte("1"))
				c.Put("b", []byte("2"))
				c.Get("a")              // a is now MRU
				c.Put("c", []byte("3")) // evicts b
			},
			wantKeep:   []string{"a", "c"},
			wantEvicts: []string{"b"},
		},
		{
			name: "update promotes recency",
			ops: func(c *LRU) {
				c.Put("a", []byte("1"))
				c.Put("b", []byte("2"))
				c.Put("a", []byte("11")) // a is now MRU
				c.Put("c", []byte("3"))  // evicts b
			},
			wantKeep:   []string{"a", "c"},
			wantEvicts: []string{"b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewLRU(2, 0)
			tt.ops(c)
			assert.Equal(t, 2, c.Len())
			for _, k := range tt.wantKeep {
				_, ok := c.Get(k)
				assert.Truef(t, ok, "expected key %q to remain", k)
			}
			for _, k := range tt.wantEvicts {
				_, ok := c.Get(k)
				assert.Falsef(t, ok, "expected key %q to be evicted", k)
			}
		})
	}
}

func TestLRU_Delete(t *testing.T) {
	c := NewLRU(4, 0)
	c.Put("a", []byte("1"))

	assert.True(t, c.Delete("a"))
	_, ok := c.Get("a")
	assert.False(t, ok)
	assert.False(t, c.Delete("a"), "deleting missing key returns false")
}

func TestLRU_TTLExpiry(t *testing.T) {
	c := NewLRU(4, 50*time.Millisecond)
	now := time.Now()
	c.now = func() time.Time { return now }

	c.Put("a", []byte("1"))
	v, ok := c.Get("a")
	require.True(t, ok)
	assert.Equal(t, []byte("1"), v)

	// Advance the clock past the TTL.
	c.now = func() time.Time { return now.Add(time.Second) }
	_, ok = c.Get("a")
	assert.False(t, ok, "entry should have expired")
	assert.Equal(t, 0, c.Len(), "expired entry should be removed on access")
}

func TestLRU_ValueIsolation(t *testing.T) {
	c := NewLRU(4, 0)
	original := []byte("hello")
	c.Put("a", original)

	// Mutating the caller's buffer must not affect the cached copy.
	original[0] = 'X'
	v, ok := c.Get("a")
	require.True(t, ok)
	assert.Equal(t, []byte("hello"), v)

	// Mutating the returned buffer must not affect the cached copy.
	v[1] = 'Y'
	v2, _ := c.Get("a")
	assert.Equal(t, []byte("hello"), v2)
}

func TestLRU_ConcurrentAccess(t *testing.T) {
	c := NewLRU(128, 0)
	var wg sync.WaitGroup
	const workers = 32
	const ops = 500

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := strconv.Itoa((id*ops + i) % 256)
				c.Put(key, []byte(key))
				c.Get(key)
				if i%7 == 0 {
					c.Delete(key)
				}
			}
		}(w)
	}
	wg.Wait()

	// Invariant: never exceeds capacity.
	assert.LessOrEqual(t, c.Len(), 128)
}
