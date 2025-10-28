package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	assert.NoError(t, cfg.Validate())
	assert.Equal(t, ":8080", cfg.Server.Addr)
	assert.Equal(t, 4096, cfg.Cache.L1Capacity)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("HERMES_SERVER_ADDR", ":9999")
	t.Setenv("HERMES_REDIS_ADDR", "redis.internal:6379")
	t.Setenv("HERMES_REDIS_TTL", "30s")
	t.Setenv("HERMES_CASSANDRA_HOSTS", "c1:9042, c2:9042 ,c3:9042")
	t.Setenv("HERMES_CACHE_L1_CAPACITY", "256")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, ":9999", cfg.Server.Addr)
	assert.Equal(t, "redis.internal:6379", cfg.Redis.Addr)
	assert.Equal(t, 30*time.Second, cfg.Redis.TTL)
	assert.Equal(t, []string{"c1:9042", "c2:9042", "c3:9042"}, cfg.Cassandra.Hosts)
	assert.Equal(t, 256, cfg.Cache.L1Capacity)
}

func TestLoad_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	contents := []byte("" +
		"server:\n" +
		"  addr: \":7000\"\n" +
		"cassandra:\n" +
		"  keyspace: mykeyspace\n" +
		"  table: mytable\n" +
		"  hosts:\n" +
		"    - db1:9042\n" +
		"cache:\n" +
		"  l1_capacity: 1024\n")
	require.NoError(t, os.WriteFile(path, contents, 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ":7000", cfg.Server.Addr)
	assert.Equal(t, "mykeyspace", cfg.Cassandra.Keyspace)
	assert.Equal(t, "mytable", cfg.Cassandra.Table)
	assert.Equal(t, []string{"db1:9042"}, cfg.Cassandra.Hosts)
	assert.Equal(t, 1024, cfg.Cache.L1Capacity)
}

func TestValidate_Errors(t *testing.T) {
	cfg := Default()
	cfg.Cache.L1Capacity = 0
	assert.Error(t, cfg.Validate())
}
