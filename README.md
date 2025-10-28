# Hermes — Low-Latency Caching Service

Hermes is a multi-threaded cloud caching service written in Go. It fronts a
**Cassandra** durable store with a **Redis** distributed cache and a per-instance
**in-process LRU**, forming a three-level read-through / write-through cache. It
applies real data structures and algorithms (an O(1) LRU, a request coalescer to
prevent cache stampedes) and is built for high concurrency using goroutines and
fine-grained synchronization.

- **Language / runtime:** Go 1.22
- **Backing store (L3):** Cassandra via [`gocql`](https://github.com/gocql/gocql)
- **Distributed cache (L2):** Redis via [`go-redis`](https://github.com/redis/go-redis)
- **In-process cache (L1):** custom concurrency-safe LRU (map + doubly-linked list)
- **API:** REST over `net/http`, routed with [`chi`](https://github.com/go-chi/chi)
- **Tests:** [Testify](https://github.com/stretchr/testify), table-driven
- **CI/CD:** Jenkins (`Jenkinsfile`) + GitLab CI (`.gitlab-ci.yml`)
- **Packaging:** multi-stage `Dockerfile`, `docker-compose.yml`, Kubernetes manifests

---

## Architecture

Hermes is a classic multi-level cache. A request enters at the fastest, smallest
tier and falls through to slower, larger, more authoritative tiers on a miss.

```
                         ┌──────────────────────────┐
   HTTP client  ───────► │   REST API (chi router)   │
                         │  GET/PUT/DELETE /v1/cache │
                         └─────────────┬─────────────┘
                                       │
                                       ▼
                         ┌──────────────────────────┐
                         │     service.Service       │
                         │  read-through / write-thru │
                         │  + request coalescer       │
                         │  + metrics counters        │
                         └─────────────┬─────────────┘
                  hit ◄────────────────┤
                                       ▼
   ┌───────────────┐   miss   ┌───────────────┐   miss   ┌────────────────┐
   │  L1: in-proc  │ ───────► │   L2: Redis   │ ───────► │ L3: Cassandra   │
   │   LRU cache   │          │  (shared,     │          │  (durable       │
   │ (ns, per-pod) │ ◄─────── │   cluster)    │ ◄─────── │   system of     │
   └───────────────┘ backfill └───────────────┘ backfill │   record)       │
                                                          └────────────────┘
```

| Tier | Backing            | Scope          | Latency      | Purpose                              |
|------|--------------------|----------------|--------------|--------------------------------------|
| L1   | in-process LRU     | per instance   | nanoseconds  | absorb hot keys, no network          |
| L2   | Redis              | cluster-shared | ~ms          | shared cache across all instances    |
| L3   | Cassandra          | cluster-shared | ~ms–10s ms   | durable system of record             |

### Read-through flow (`GET`)

1. Check **L1** (LRU). On hit, return immediately.
2. On L1 miss, enter the **request coalescer** keyed by the cache key, then:
   1. Check **L2** (Redis). On hit, back-fill **L1** and return.
   2. On L2 miss, check **L3** (Cassandra). On hit, back-fill **L2** and **L1**
      and return.
   3. On L3 miss, return *not found*.

Back-filling on the way up means a value fetched from a slow tier is promoted
into every faster tier, so subsequent reads are served as close to the edge as
possible.

### Write-through flow (`PUT` / `DELETE`)

Writes always hit the **durable store first** so Cassandra is authoritative, then
propagate to the caches:

- **PUT:** write L3 → refresh L2 → update L1. Updating (rather than just
  invalidating) the caches keeps reads warm and avoids a guaranteed follow-up
  miss.
- **DELETE:** delete L3 → delete L2 → delete L1, so no stale value can be served
  from any tier.

A failure to refresh a *cache* tier (L1/L2) after a successful durable write is
**non-fatal**: correctness is preserved because the next read falls through to
L3. Such failures are surfaced through the error metric.

### Request coalescing (anti-stampede)

When a hot key expires, many concurrent requests can miss the cache
simultaneously and stampede the backing store ("thundering herd"). Hermes
includes a small in-house **coalescer** (`internal/service/coalescer.go`), a
singleflight-style mechanism built from a `sync.Mutex` and per-key
`sync.WaitGroup`:

- The first caller for a key registers an in-flight `call` and executes the
  single backing fetch.
- Concurrent callers for the same key attach to the existing `call` and block on
  its wait group instead of issuing their own backing request.
- When the fetch completes, the result is fanned out to every waiter and the
  in-flight entry is removed so the next wave triggers a fresh fetch.

This collapses **N concurrent misses for one key into a single backing fetch**,
which is verified by `TestService_Get_CoalescesConcurrentMisses` and
`TestCoalescer_ConcurrentCallersShareOneFetch`.

### Concurrency model

- The **LRU** guards its map + linked list with a single `sync.Mutex`; all
  operations are O(1) so lock hold times are minimal. Values are copied in and
  out so callers can never mutate cached buffers.
- **Metrics** use `sync/atomic` counters, so the hot path never blocks on a lock
  for bookkeeping.
- The **coalescer** uses a mutex only to register/deregister in-flight calls;
  the actual backing fetch runs outside the lock.

---

## API Reference

| Method   | Path                | Request body        | Success         | Notes                                   |
|----------|---------------------|---------------------|-----------------|-----------------------------------------|
| `GET`    | `/v1/cache/{key}`   | —                   | `200` + bytes   | `404` if absent in all tiers            |
| `PUT`    | `/v1/cache/{key}`   | raw bytes (≤ 4 MiB) | `204 No Content`| write-through to L3 + caches            |
| `DELETE` | `/v1/cache/{key}`   | —                   | `204 No Content`| write-through delete across all tiers   |
| `GET`    | `/healthz`          | —                   | `200`/`503`     | pings backing dependencies (e.g. Redis) |
| `GET`    | `/metrics`          | —                   | `200` + JSON    | per-layer hit/miss + latency snapshot   |

Values are treated as opaque bytes (`application/octet-stream`); store whatever
you like. Error responses are JSON: `{"error": "..."}`.

Example:

```bash
curl -X PUT  --data-binary 'hello' http://localhost:8080/v1/cache/greeting
curl         http://localhost:8080/v1/cache/greeting        # -> hello
curl         http://localhost:8080/metrics                  # -> {...}
curl -X DELETE http://localhost:8080/v1/cache/greeting
```

`/metrics` snapshot fields include `l1_hits`, `l1_misses`, `l2_hits`,
`l2_misses`, `l3_hits`, `l3_misses`, `writes`, `deletes`, `coalesced_requests`,
`errors`, `requests`, `avg_latency_micros`, and `overall_hit_ratio`.

---

## Configuration

Config is loaded from defaults, then an optional YAML file (`--config` or
`HERMES_CONFIG`), then environment variables. **Environment wins over file wins
over defaults.** See `config.example.yaml`.

| Env var                          | File key                  | Default            | Description                              |
|----------------------------------|---------------------------|--------------------|------------------------------------------|
| `HERMES_SERVER_ADDR`             | `server.addr`             | `:8080`            | HTTP listen address                      |
| `HERMES_SERVER_READ_TIMEOUT`     | `server.read_timeout`     | `5s`               | HTTP read timeout                        |
| `HERMES_SERVER_WRITE_TIMEOUT`    | `server.write_timeout`    | `10s`              | HTTP write timeout                       |
| `HERMES_SERVER_SHUTDOWN_TIMEOUT` | `server.shutdown_timeout` | `15s`              | graceful shutdown grace period           |
| `HERMES_REDIS_ADDR`              | `redis.addr`              | `localhost:6379`   | Redis (L2) address                       |
| `HERMES_REDIS_PASSWORD`          | `redis.password`          | *(empty)*          | Redis password                           |
| `HERMES_REDIS_DB`                | `redis.db`                | `0`                | Redis logical DB                         |
| `HERMES_REDIS_TTL`               | `redis.ttl`               | `10m`              | TTL for values written to Redis          |
| `HERMES_CASSANDRA_HOSTS`         | `cassandra.hosts`         | `localhost:9042`   | comma-separated contact points           |
| `HERMES_CASSANDRA_KEYSPACE`      | `cassandra.keyspace`      | `hermes`           | keyspace                                 |
| `HERMES_CASSANDRA_TABLE`         | `cassandra.table`         | `records`          | KV table name                            |
| `HERMES_CASSANDRA_CONSISTENCY`   | `cassandra.consistency`   | `QUORUM`           | read/write consistency level             |
| `HERMES_CASSANDRA_TIMEOUT`       | `cassandra.timeout`       | `5s`               | query/connect timeout                    |
| `HERMES_CASSANDRA_NUM_CONNS`     | `cassandra.num_conns`     | `4`                | connections per host                     |
| `HERMES_CACHE_L1_CAPACITY`       | `cache.l1_capacity`       | `4096`             | max entries in the in-process LRU        |
| `HERMES_CACHE_L1_TTL`            | `cache.l1_ttl`            | `1m`               | per-entry TTL in the LRU (`0` = no TTL)  |

---

## Project Layout

```
hermes/
├── cmd/hermes/main.go          # wiring + graceful shutdown
├── internal/
│   ├── config/                 # env+file config with defaults & validation
│   ├── metrics/                # atomic hit/miss/latency counters + snapshot
│   ├── cache/
│   │   ├── lru.go              # O(1) concurrency-safe LRU (L1)
│   │   └── redis.go           # go-redis wrapper implementing L2Cache (L2)
│   ├── store/store.go          # gocql Cassandra KV store (L3)
│   ├── service/
│   │   ├── service.go         # read-through / write-through across L1→L2→L3
│   │   └── coalescer.go       # singleflight-style anti-stampede coalescer
│   └── api/api.go              # chi HTTP handlers
├── deploy/
│   ├── schema.cql              # Cassandra keyspace + table
│   └── k8s/                    # Deployment, Service, ConfigMap
├── Dockerfile                  # multi-stage build -> distroless
├── docker-compose.yml          # Cassandra + Redis + hermes for local dev
├── Jenkinsfile                 # declarative CI/CD pipeline
├── .gitlab-ci.yml              # mirror of the Jenkins pipeline
└── config.example.yaml
```

> The packages depend on the backing tiers through interfaces
> (`cache.L2Cache`, `store.Store`, `api.Cacher`), so the service and API layers
> are unit-tested with in-memory fakes — no live Cassandra/Redis required.

---

## Build, Test, Run

> The commands below are documented for reference. They are **not executed** as
> part of generating this repository.

### Build & test locally

```bash
go build ./...
go build -o bin/hermes ./cmd/hermes
go test ./...                 # all unit tests (fakes, no external deps)
go test -race ./...           # race detector (recommended; concurrency-heavy)
go test -cover ./...          # coverage
go vet ./...
```

### Run locally (with Docker Compose deps)

`docker-compose.yml` brings up Cassandra, applies the schema via a one-shot
init job, brings up Redis, then builds and runs Hermes:

```bash
docker compose up --build
# Hermes on http://localhost:8080
```

To run the binary directly against your own Cassandra/Redis:

```bash
# apply schema once:
cqlsh -f deploy/schema.cql

export HERMES_REDIS_ADDR=localhost:6379
export HERMES_CASSANDRA_HOSTS=localhost:9042
go run ./cmd/hermes --config config.example.yaml
```

### Container image

```bash
docker build -t hermes:dev .
docker run --rm -p 8080:8080 \
  -e HERMES_REDIS_ADDR=redis:6379 \
  -e HERMES_CASSANDRA_HOSTS=cassandra:9042 \
  hermes:dev
```

### Kubernetes

```bash
kubectl create namespace hermes
kubectl apply -f deploy/k8s/
```

The `Deployment` runs 3 replicas (read-only root FS, non-root, dropped
capabilities) with `/healthz` readiness/liveness probes, and pulls its
environment from the `hermes-config` `ConfigMap`. Each pod keeps its own L1 LRU;
all pods share L2 (Redis) and L3 (Cassandra).

---

## CI/CD Pipeline

Both `Jenkinsfile` (declarative) and `.gitlab-ci.yml` implement the same stages:

1. **Checkout** — fetch source.
2. **Lint** — `go vet`, `gofmt` check, `staticcheck`.
3. **Test** — `go test -race -covermode=atomic -coverprofile=coverage.out ./...`,
   report total coverage; archive `coverage.out`.
4. **Build** — `go build -trimpath -ldflags="-s -w" -o bin/hermes ./cmd/hermes`,
   archive the artifact.
5. **Docker Build & Push** — build the multi-stage image and push
   `:<git-sha>` and `:latest` to the registry (only on `main` / tags).
6. **Deploy** — `kubectl set image` + `kubectl rollout status` against the
   production cluster (only on `main`).

Registry credentials and kubeconfig are injected from the CI system's secret
store; nothing sensitive is committed (see `.gitignore`).
