# Design decisions

Short, append-only log of non-obvious choices. Each entry names the decision,
the alternatives that were considered, and the reason. Entries are dated so a
reader can tell when the reasoning is stale.

Format: `## <date> — <decision>`, then a paragraph or two. Do not delete
entries; if a decision is reversed, add a new entry that supersedes it and
link back.

---

## 2026-09-21 — Atomic Redis Lua scripts, not distributed locks

Every rate-limit read-modify-write happens inside a Lua script called via
`EVALSHA`. Redis runs Lua atomically — no other command interleaves between
steps — so the counter update is race-free without acquiring an external
lock. Alternatives considered:

- **Distributed lock (e.g. Redsync)** — real correctness cost (fencing tokens,
  clock-skew handling) and a whole additional failure mode.
- **`WATCH`/`MULTI`/`EXEC` optimistic transactions** — retries on contention
  would blow up latency under burst load, the exact case rate limiters need
  to handle well.

Scripts are preloaded at connection time and cached by SHA; a `NOSCRIPT`
error triggers a re-load without changing the caller's contract. See
`internal/store/scripts/` for the three scripts.

## 2026-09-21 — Fail-open vs fail-closed is configurable, not hard-coded

When Redis is unreachable, the middleware falls back to whatever `fail_mode`
says: `closed` rejects everything, `open` allows everything. This isn't a
hedge — the correct answer genuinely depends on the endpoint:

- **`closed`** for write-heavy or billing endpoints: uncapped writes into an
  outage are worse than a temporary rejection wave.
- **`open`** for read-only public endpoints: rejecting all reads because the
  limiter's state store is down is a bigger customer impact than briefly
  serving unrated traffic.

Default is `closed` because that's the safer choice when a project owner
hasn't thought it through yet.

## 2026-09-21 — Three algorithms behind one interface

Token bucket, fixed window, and sliding-window-log all implement
`algorithms.Limiter`. This costs a tiny amount of dispatch overhead and buys:

1. Rules can pick their algorithm without the middleware branching on the
   algorithm name at request time.
2. The 50-goroutine burst test in `tests/concurrent/` runs the same shape
   against all three, so a bug in the interface contract shows up once
   rather than three times.
3. Adding a fourth algorithm (leaky-bucket, GCRA) is a single new file, no
   changes to the middleware or the store.

## 2026-09-21 — Two `Store` implementations behind one interface

`store.RedisStore` (production) and `store.Fake` (tests) satisfy the same
`Store` interface. Fake models the atomicity by locking the entire store per
call, which matches the all-or-nothing semantics `EVAL` gives us. The trade
is that Fake is coarser than Redis under contention, but tests care about
correctness, not throughput. This lets unit + concurrent tests run without
Redis, and the integration suite runs the exact same algorithms against a
real Redis via `testcontainers-go` (or `REDIS_ADDR` when set).

## 2026-09-21 — CI lint temporarily removed (was: golangci-lint v1.61 → v2.1.6 migration)

The golangci-lint action was churning on the v1 → v2 rewrite: v6 of the
action stopped running on Node 24, v7+ requires a v2 config, and the v2
config schema differs enough that migrating cost more than the lint was
catching. Removed the lint job for now; `go vet` still runs in CI as a
static-analysis floor. Revisit when we can pin a stable v2 setup or when a
lint finding actually costs us.
