-- Hermes L3 backing store schema.
-- For local/dev (single-node) we use SimpleStrategy; production should use
-- NetworkTopologyStrategy with an appropriate replication factor per DC.

CREATE KEYSPACE IF NOT EXISTS hermes
  WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};

CREATE TABLE IF NOT EXISTS hermes.records (
    key   text PRIMARY KEY,
    value blob
);
