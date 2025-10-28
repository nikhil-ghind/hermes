package cache

import (
	"sync"
	"time"
)

// entry is a node in the intrusive doubly-linked list backing the LRU. Each
// node simultaneously lives in the map (for O(1) lookup) and in the list (for
// O(1) recency reordering and eviction).
type entry struct {
	key       string
	value     []byte
	expiresAt time.Time // zero value means "never expires"
	prev      *entry
	next      *entry
}

// expired reports whether the entry has a TTL that has elapsed.
func (e *entry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// LRU is a concurrency-safe, fixed-capacity least-recently-used cache used as
// the L1 (in-process) layer of the Hermes cache hierarchy.
//
// It is implemented with a hash map for O(1) key lookup and an intrusive
// doubly-linked list for O(1) recency tracking and eviction. A sentinel-free
// design with explicit head/tail pointers is used: head is the most-recently
// used entry, tail is the least-recently used.
//
// All operations take a single mutex; the operations themselves are O(1) so
// lock hold times stay short even under contention.
type LRU struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	items    map[string]*entry
	head     *entry // most recently used
	tail     *entry // least recently used
	now      func() time.Time
}

// NewLRU constructs an LRU with the given capacity and default TTL. A ttl of 0
// disables expiry. Capacity must be positive.
func NewLRU(capacity int, ttl time.Duration) *LRU {
	if capacity <= 0 {
		capacity = 1
	}
	return &LRU{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*entry, capacity),
		now:      time.Now,
	}
}

// Get returns the value for key and whether it was present (and unexpired). A
// successful Get promotes the entry to most-recently-used. Expired entries are
// removed and reported as a miss.
func (c *LRU) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if e.expired(c.now()) {
		c.removeLocked(e)
		return nil, false
	}
	c.moveToFrontLocked(e)
	// Return a copy so callers cannot mutate the cached buffer.
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

// Put inserts or updates key with value, promoting it to most-recently-used and
// evicting the least-recently-used entry if capacity is exceeded.
func (c *LRU) Put(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stored := make([]byte, len(value))
	copy(stored, value)

	var exp time.Time
	if c.ttl > 0 {
		exp = c.now().Add(c.ttl)
	}

	if e, ok := c.items[key]; ok {
		e.value = stored
		e.expiresAt = exp
		c.moveToFrontLocked(e)
		return
	}

	e := &entry{key: key, value: stored, expiresAt: exp}
	c.items[key] = e
	c.pushFrontLocked(e)

	if len(c.items) > c.capacity {
		c.evictLocked()
	}
}

// Delete removes key from the cache, returning whether it was present.
func (c *LRU) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.items[key]
	if !ok {
		return false
	}
	c.removeLocked(e)
	return true
}

// Len returns the current number of entries.
func (c *LRU) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// --- internal list operations (must be called with c.mu held) ---

// pushFrontLocked links e at the head of the list.
func (c *LRU) pushFrontLocked(e *entry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

// unlinkLocked detaches e from the list without touching the map.
func (c *LRU) unlinkLocked(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

// moveToFrontLocked promotes an existing entry to most-recently-used.
func (c *LRU) moveToFrontLocked(e *entry) {
	if c.head == e {
		return
	}
	c.unlinkLocked(e)
	c.pushFrontLocked(e)
}

// removeLocked unlinks e and deletes it from the map.
func (c *LRU) removeLocked(e *entry) {
	c.unlinkLocked(e)
	delete(c.items, e.key)
}

// evictLocked removes the least-recently-used entry (the tail).
func (c *LRU) evictLocked() {
	if c.tail == nil {
		return
	}
	c.removeLocked(c.tail)
}
