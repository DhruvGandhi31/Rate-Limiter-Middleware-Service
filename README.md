# Rate Limiter Middleware Service

Distributed rate-limiting service in Go, backed by Redis for multi-instance consistency.
Implements three algorithms — Token Bucket, Fixed Window, Sliding Window Log — behind a
single HTTP API. All state lives in Redis and every read-modify-write is an atomic Lua
script, so two service instances make consistent decisions under concurrent load.

---

## Architecture

```
            ┌───────────────────────┐
            │      HTTP client      │
            └──────────┬────────────┘
                       │
                       ▼
            ┌───────────────────────┐
            │  chi router + API     │   POST /check  GET /status  POST /reset
            │  (api/handlers.go)    │   GET  /health
            └──────────┬────────────┘
                       │
                       ▼
            ┌───────────────────────┐
            │  Limiter middleware   │   matches rule → bucket key
            │  (internal/middleware)│   fail_open / fail_closed policy
            └──────────┬────────────┘
                       │
                       ▼
            ┌───────────────────────┐
            │  Algorithm registry   │   token_bucket / fixed_window / sliding_window
            │  (internal/algorithms)│
            └──────────┬────────────┘
                       │  atomic EVAL of Lua script
                       ▼
            ┌───────────────────────┐
            │        Redis          │   shared across N instances
            └───────────────────────┘
```

**Why Lua:** Redis runs Lua scripts atomically — no other command interleaves between
steps. That replaces the need for a distributed lock around the read-modify-write on
the rate counter.

**Key namespace:** `ratelimit:<algorithm>:<identifier>:<rule>` — easy to inspect with
`redis-cli KEYS 'ratelimit:*'`.

---

## Algorithm comparison

| Algorithm | Burst behavior | Accuracy | Memory / client | When to use |
|-----------|----------------|----------|-----------------|-------------|
| **Token bucket**  | Bursts up to capacity, then refills at fixed rate | Exact within bucket math | O(1) (hash of 2 fields) | APIs that want to tolerate short spikes |
| **Fixed window**  | Resets at wall-clock boundary | Exact within a window; can allow ~2× at boundary | O(1) (single counter) | Cheap, coarse limits; analytics-style traffic |
| **Sliding window log** | Smooth — every request ages out individually | Most accurate | O(N) (one sorted-set entry per request in window) | Strict per-window guarantees |

The boundary burst on fixed window is real and intentionally [documented in a test](tests/unit/boundary_test.go).

---

## HTTP API

All rejected responses include the Stripe-style headers:

```
HTTP/1.1 429 Too Many Requests
Retry-After: 3
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1715900000
```

| Method | Endpoint        | Auth   | Description                                              |
|--------|-----------------|--------|----------------------------------------------------------|
| POST   | `/check`        | none   | Consume a token. Body: `{"rule":"name","identifier":"u"}` |
| GET    | `/status/:key`  | none   | Inspect state without consuming. `?rule=name`             |
| POST   | `/reset/:key`   | Bearer | Admin reset. `?rule=name`. Requires `Authorization: Bearer <token>` |
| GET    | `/health`       | none   | Liveness + Redis connectivity                             |
| GET    | `/metrics`      | none   | Prometheus exposition (scrape this)                       |

The same logic is also available as Go middleware (`limiter.HTTPMiddleware(next)`),
which lets you front any `http.Handler` directly without going through `/check`.

---

## Configuration

YAML config (env vars override). See [`config.yaml`](config.yaml).

```yaml
fail_mode: "closed"   # "closed" rejects on Redis outage; "open" allows
rules:
  - name: "burst-tolerant"
    identifier: "api_key"   # api_key | ip | endpoint
    match: "^/api/"         # regex over r.URL.Path (empty = match all)
    algorithm: "token_bucket"
    limit: 100              # bucket capacity
    refill_rate: 10         # tokens/sec
```

**Environment overrides:** `REDIS_ADDR`, `REDIS_PASSWORD`, `SERVER_ADDR`, `FAIL_MODE`,
`ADMIN_TOKEN`, `LOG_LEVEL`, `LOG_FORMAT`.

## Observability

Every limiter decision is instrumented. Scrape `GET /metrics` from Prometheus:

| Metric                                       | Type      | Labels                       | What it tells you             |
|---------------------------------------------|-----------|------------------------------|-------------------------------|
| `ratelimit_requests_total`                  | counter   | `rule`, `algorithm`, `decision` (`allowed`/`rejected`/`error`) | reject-rate, error-rate per rule |
| `ratelimit_decision_duration_seconds`       | histogram | `rule`, `algorithm`          | limiter latency (Redis EVAL)  |
| `ratelimit_redis_errors_total`              | counter   | `rule`                       | Redis unavailability — alert on this |
| `ratelimit_http_requests_total`             | counter   | `route`, `status` (2xx/4xx/5xx) | HTTP-surface traffic mix      |
| `ratelimit_http_request_duration_seconds`   | histogram | `route`                      | end-to-end HTTP latency       |

Suggested alerts (Prometheus rules):

```yaml
- alert: RateLimiterRedisDown
  expr: rate(ratelimit_redis_errors_total[1m]) > 0
  for: 30s
- alert: RateLimiterErrorBudgetBurn
  expr: rate(ratelimit_requests_total{decision="error"}[5m]) > 1
```

Logs are structured JSON via `log/slog`. Configure level/format in `config.yaml` or
via `LOG_LEVEL` / `LOG_FORMAT` env vars. Every HTTP request emits an access log
with method, route, status, and duration.

### Fail-open vs fail-closed

This is configurable because there's no universally correct answer:

- **fail-closed** (default) — Redis outage means *all requests are rejected*. Pick this
  when an unbounded request volume would damage the upstream more than a brief
  outage damages the user (e.g. write-heavy APIs, billing endpoints).
- **fail-open** — Redis outage means *all requests pass*. Pick this when availability
  trumps correctness (e.g. read-only public endpoints where uncapped traffic is
  expensive but not catastrophic).

---

## Running locally

```bash
# 1. Start Redis + the service together
docker compose up --build

# 2. Hit it
curl -s -X POST localhost:8080/check \
    -H 'content-type: application/json' \
    -d '{"rule":"burst-tolerant","identifier":"user-42"}' | jq

# 3. Inspect state
curl -s localhost:8080/status/user-42?rule=burst-tolerant | jq

# 4. Reset
curl -s -X POST localhost:8080/reset/user-42?rule=burst-tolerant
```

Or run without Docker:

```bash
go run ./cmd/server -config ./config.yaml
```

---

## Tests

```bash
# Unit + concurrent (no Redis required — uses an in-memory fake)
go test ./tests/unit/... ./tests/concurrent/...

# Integration (requires Redis — auto-skips if unreachable)
REDIS_ADDR=localhost:6379 go test ./tests/integration/...

# Everything with race detector
go test -race ./...
```

What's covered:

- **Unit** — each algorithm in isolation; HTTP handlers; fail-open vs fail-closed.
- **Concurrent burst** — 50 goroutines hammer one key; assert *exactly* `limit`
  succeed across all three algorithms.
- **Boundary** — fixed-window 2× burst across window edge, asserted observable.
- **Redis failure** — fake store returns errors; assert decision matches `fail_mode`.
- **Integration** — real Redis end-to-end, including a full HTTP request flow.

---

## Benchmarks

Three benchmark layers, in order of how close they are to a real deployment:

```bash
# 1. Pure-algorithm baseline (in-memory fake, no I/O)
go test -bench=Fake -benchmem ./bench/...

# 2. Algorithm + Redis round-trip
REDIS_ADDR=localhost:6379 go test -bench=Redis -benchmem ./bench/...

# 3. Full HTTP stack (router + JSON + middleware + algorithm + Redis)
REDIS_ADDR=localhost:6379 go test -bench=HTTP  -benchmem ./bench/...

# 4. Operational percentiles (real TCP, p50/p95/p99)
REDIS_ADDR=localhost:6379 go test -run HTTPLatency -v ./bench/...
```

### Results

Measured on a 13th Gen Intel Core i7-13650HX (Windows, GOMAXPROCS=20), against
a local Dockerized Redis 7. **The HTTP and Redis-EVAL numbers come from `b.RunParallel`
across 20 cores**, so the per-op latency is wall-clock per concurrent operation.

**Per-decision latency (Redis EVAL only):**

| Algorithm        | Latency (ns/op) | Allocations/op |
|------------------|-----------------|----------------|
| Token bucket     | 36,566          | 11             |
| Fixed window     | 31,613          | 15             |
| Sliding window   | 36,487          | 18             |

**Full HTTP stack (handler → middleware → Redis):**

| Algorithm        | Latency (ns/op) | Allocations/op |
|------------------|-----------------|----------------|
| Token bucket     | 42,058          | 61             |
| Fixed window     | 42,256          | 65             |
| Sliding window   | 44,542          | 68             |

**Operational percentiles (real TCP, concurrency=20, 10,000 requests):**

| Metric         | Token bucket |
|----------------|--------------|
| Throughput     | **7,919 req/s** (single-host, single-Redis) |
| p50            | 2.3 ms       |
| p95            | 4.5 ms       |
| p99            | 5.7 ms       |
| p99.9          | 17.8 ms      |

Throughput scales horizontally with service instances behind a load balancer; the
single bottleneck is Redis. With Redis Cluster (key-sharded) the same numbers per
shard apply linearly.

### Running a real load test

```bash
docker compose up --build
./bench/load_test.sh                              # 30s @ c=100
DURATION=60s CONCURRENCY=200 ./bench/load_test.sh
```

---
