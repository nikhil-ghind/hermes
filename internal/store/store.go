// Package store implements the L3 backing store for Hermes on top of a
// Cassandra cluster using the gocql driver. Records are stored in a simple
// key/value table with prepared CQL statements for Get/Put/Delete.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// ErrNotFound is returned by Get when no row exists for the requested key.
var ErrNotFound = errors.New("store: key not found")

// Store is the interface the service layer depends on for the backing store.
// Defining it as an interface lets tests substitute an in-memory fake.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	Close()
}

// CassandraStore is a gocql-backed implementation of Store.
type CassandraStore struct {
	session *gocql.Session
	table   string

	getStmt string
	putStmt string
	delStmt string
}

// Options configures a CassandraStore.
type Options struct {
	Hosts       []string
	Keyspace    string
	Table       string
	Consistency string
	Timeout     time.Duration
	NumConns    int
}

// NewCassandraStore connects to the cluster and prepares the CQL statements.
// It assumes the keyspace and table already exist (see deploy/schema in the
// README / docker-compose init). The table schema is:
//
//	CREATE TABLE <keyspace>.<table> (
//	    key   text PRIMARY KEY,
//	    value blob
//	);
func NewCassandraStore(opts Options) (*CassandraStore, error) {
	cluster := gocql.NewCluster(opts.Hosts...)
	cluster.Keyspace = opts.Keyspace
	cluster.Consistency = parseConsistency(opts.Consistency)
	if opts.Timeout > 0 {
		cluster.Timeout = opts.Timeout
		cluster.ConnectTimeout = opts.Timeout
	}
	if opts.NumConns > 0 {
		cluster.NumConns = opts.NumConns
	}

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("cassandra: create session: %w", err)
	}

	table := opts.Table
	s := &CassandraStore{
		session: session,
		table:   table,
		getStmt: fmt.Sprintf("SELECT value FROM %s WHERE key = ? LIMIT 1", table),
		putStmt: fmt.Sprintf("INSERT INTO %s (key, value) VALUES (?, ?)", table),
		delStmt: fmt.Sprintf("DELETE FROM %s WHERE key = ?", table),
	}
	return s, nil
}

// Get retrieves the value for key, returning ErrNotFound when absent.
func (s *CassandraStore) Get(ctx context.Context, key string) ([]byte, error) {
	var value []byte
	err := s.session.Query(s.getStmt, key).WithContext(ctx).Scan(&value)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cassandra get %q: %w", key, err)
	}
	return value, nil
}

// Put upserts the value for key.
func (s *CassandraStore) Put(ctx context.Context, key string, value []byte) error {
	if err := s.session.Query(s.putStmt, key, value).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("cassandra put %q: %w", key, err)
	}
	return nil
}

// Delete removes key. Deleting a missing key is not an error in Cassandra.
func (s *CassandraStore) Delete(ctx context.Context, key string) error {
	if err := s.session.Query(s.delStmt, key).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("cassandra delete %q: %w", key, err)
	}
	return nil
}

// Close shuts down the gocql session.
func (s *CassandraStore) Close() {
	if s.session != nil {
		s.session.Close()
	}
}

// parseConsistency maps a textual consistency level to gocql's enum, defaulting
// to QUORUM for unknown values.
func parseConsistency(s string) gocql.Consistency {
	switch s {
	case "ANY":
		return gocql.Any
	case "ONE":
		return gocql.One
	case "TWO":
		return gocql.Two
	case "THREE":
		return gocql.Three
	case "QUORUM":
		return gocql.Quorum
	case "ALL":
		return gocql.All
	case "LOCAL_QUORUM":
		return gocql.LocalQuorum
	case "EACH_QUORUM":
		return gocql.EachQuorum
	case "LOCAL_ONE":
		return gocql.LocalOne
	default:
		return gocql.Quorum
	}
}
