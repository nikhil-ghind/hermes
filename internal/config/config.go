// Package config loads Hermes configuration from an optional YAML/JSON file and
// environment variables, applying sensible defaults. Environment variables
// always take precedence over file values, which take precedence over defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for the Hermes service.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Redis     RedisConfig     `yaml:"redis"`
	Cassandra CassandraConfig `yaml:"cassandra"`
	Cache     CacheConfig     `yaml:"cache"`
}

// ServerConfig configures the HTTP API server.
type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// RedisConfig configures the L2 Redis cache client.
type RedisConfig struct {
	Addr     string        `yaml:"addr"`
	Password string        `yaml:"password"`
	DB       int           `yaml:"db"`
	TTL      time.Duration `yaml:"ttl"`
}

// CassandraConfig configures the L3 Cassandra backing store.
type CassandraConfig struct {
	Hosts       []string      `yaml:"hosts"`
	Keyspace    string        `yaml:"keyspace"`
	Table       string        `yaml:"table"`
	Consistency string        `yaml:"consistency"`
	Timeout     time.Duration `yaml:"timeout"`
	NumConns    int           `yaml:"num_conns"`
}

// CacheConfig configures the in-process L1 LRU cache.
type CacheConfig struct {
	L1Capacity int           `yaml:"l1_capacity"`
	L1TTL      time.Duration `yaml:"l1_ttl"`
}

// Default returns a Config populated entirely with default values.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:            ":8080",
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    10 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Redis: RedisConfig{
			Addr: "localhost:6379",
			DB:   0,
			TTL:  10 * time.Minute,
		},
		Cassandra: CassandraConfig{
			Hosts:       []string{"localhost:9042"},
			Keyspace:    "hermes",
			Table:       "records",
			Consistency: "QUORUM",
			Timeout:     5 * time.Second,
			NumConns:    4,
		},
		Cache: CacheConfig{
			L1Capacity: 4096,
			L1TTL:      time.Minute,
		},
	}
}

// Load builds a Config by starting from defaults, optionally overlaying a YAML
// file (if path is non-empty and exists), then applying environment overrides.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config file %q: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overlays environment variables onto cfg. All variables are prefixed
// with HERMES_.
func applyEnv(cfg *Config) {
	if v := os.Getenv("HERMES_SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if d, ok := envDuration("HERMES_SERVER_READ_TIMEOUT"); ok {
		cfg.Server.ReadTimeout = d
	}
	if d, ok := envDuration("HERMES_SERVER_WRITE_TIMEOUT"); ok {
		cfg.Server.WriteTimeout = d
	}
	if d, ok := envDuration("HERMES_SERVER_SHUTDOWN_TIMEOUT"); ok {
		cfg.Server.ShutdownTimeout = d
	}

	if v := os.Getenv("HERMES_REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v, ok := os.LookupEnv("HERMES_REDIS_PASSWORD"); ok {
		cfg.Redis.Password = v
	}
	if n, ok := envInt("HERMES_REDIS_DB"); ok {
		cfg.Redis.DB = n
	}
	if d, ok := envDuration("HERMES_REDIS_TTL"); ok {
		cfg.Redis.TTL = d
	}

	if v := os.Getenv("HERMES_CASSANDRA_HOSTS"); v != "" {
		cfg.Cassandra.Hosts = splitAndTrim(v)
	}
	if v := os.Getenv("HERMES_CASSANDRA_KEYSPACE"); v != "" {
		cfg.Cassandra.Keyspace = v
	}
	if v := os.Getenv("HERMES_CASSANDRA_TABLE"); v != "" {
		cfg.Cassandra.Table = v
	}
	if v := os.Getenv("HERMES_CASSANDRA_CONSISTENCY"); v != "" {
		cfg.Cassandra.Consistency = v
	}
	if d, ok := envDuration("HERMES_CASSANDRA_TIMEOUT"); ok {
		cfg.Cassandra.Timeout = d
	}
	if n, ok := envInt("HERMES_CASSANDRA_NUM_CONNS"); ok {
		cfg.Cassandra.NumConns = n
	}

	if n, ok := envInt("HERMES_CACHE_L1_CAPACITY"); ok {
		cfg.Cache.L1Capacity = n
	}
	if d, ok := envDuration("HERMES_CACHE_L1_TTL"); ok {
		cfg.Cache.L1TTL = d
	}
}

// Validate checks that the configuration is internally consistent.
func (c Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr must not be empty")
	}
	if len(c.Cassandra.Hosts) == 0 {
		return fmt.Errorf("cassandra.hosts must not be empty")
	}
	if c.Cassandra.Keyspace == "" {
		return fmt.Errorf("cassandra.keyspace must not be empty")
	}
	if c.Cassandra.Table == "" {
		return fmt.Errorf("cassandra.table must not be empty")
	}
	if c.Cache.L1Capacity <= 0 {
		return fmt.Errorf("cache.l1_capacity must be positive, got %d", c.Cache.L1Capacity)
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis.addr must not be empty")
	}
	return nil
}

func envDuration(key string) (time.Duration, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false
	}
	return d, true
}

func envInt(key string) (int, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func splitAndTrim(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
