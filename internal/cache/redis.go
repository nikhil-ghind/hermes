package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound is returned by RedisCache.Get when the key is absent. Callers
// should treat it as a cache miss rather than an error.
var ErrNotFound = errors.New("cache: key not found")

// L2Cache is the abstraction the service layer depends on for the Redis tier.
// It is defined as an interface so tests can substitute an in-memory fake.
type L2Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Ping(ctx context.Context) error
	Close() error
}

// RedisCache is a thin wrapper over go-redis implementing L2Cache. It namespaces
// keys to avoid collisions with other tenants of the same Redis instance.
type RedisCache struct {
	client    *redis.Client
	keyPrefix string
}

// RedisOptions configures a RedisCache.
type RedisOptions struct {
	Addr      string
	Password  string
	DB        int
	KeyPrefix string
}

// NewRedisCache constructs a RedisCache from the given options.
func NewRedisCache(opts RedisOptions) *RedisCache {
	client := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Password: opts.Password,
		DB:       opts.DB,
	})
	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = "hermes:"
	}
	return &RedisCache{client: client, keyPrefix: prefix}
}

func (r *RedisCache) namespaced(key string) string {
	return r.keyPrefix + key
}

// Get fetches a value, returning ErrNotFound on a cache miss.
func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, r.namespaced(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("redis get %q: %w", key, err)
	}
	return b, nil
}

// Set stores a value with the provided TTL (0 means no expiry).
func (r *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, r.namespaced(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

// Delete removes a key. Deleting a missing key is not an error.
func (r *RedisCache) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, r.namespaced(key)).Err(); err != nil {
		return fmt.Errorf("redis del %q: %w", key, err)
	}
	return nil
}

// Ping verifies connectivity to Redis.
func (r *RedisCache) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close releases the underlying connection pool.
func (r *RedisCache) Close() error {
	return r.client.Close()
}
