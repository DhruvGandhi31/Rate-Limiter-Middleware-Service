package store

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Fake is an in-memory Store implementation that mirrors the Redis Lua-script
// semantics for the three algorithms. Used by unit and concurrent tests so they
// don't need a running Redis. It intentionally locks the entire store per call,
// which models the all-or-nothing atomicity Redis gives us with EVAL.
type Fake struct {
	mu          sync.Mutex
	hashes      map[string]map[string]string
	sortedSets  map[string][]sortedEntry
	counters    map[string]*counter
	failNext    bool
	failForever bool
}

type sortedEntry struct {
	score  int64
	member string
}

type counter struct {
	value   int64
	expires time.Time
}

func NewFake() *Fake {
	return &Fake{
		hashes:     make(map[string]map[string]string),
		sortedSets: make(map[string][]sortedEntry),
		counters:   make(map[string]*counter),
	}
}

// FailNext makes the next script call return ErrRedisUnavailable. Use in
// failure-mode tests.
func (f *Fake) FailNext()    { f.mu.Lock(); f.failNext = true; f.mu.Unlock() }
func (f *Fake) FailForever() { f.mu.Lock(); f.failForever = true; f.mu.Unlock() }
func (f *Fake) Heal()        { f.mu.Lock(); f.failForever = false; f.failNext = false; f.mu.Unlock() }

func (f *Fake) shouldFail() bool {
	if f.failForever {
		return true
	}
	if f.failNext {
		f.failNext = false
		return true
	}
	return false
}

func (f *Fake) EvalTokenBucket(ctx context.Context, key string, capacity int64, refillRate float64, nowMs, requested, ttlSec int64) (ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shouldFail() {
		return ScriptResult{}, ErrRedisUnavailable
	}

	h, ok := f.hashes[key]
	if !ok {
		h = map[string]string{}
		f.hashes[key] = h
	}
	tokens := float64(capacity)
	lastRefill := nowMs
	if t, ok := h["tokens"]; ok {
		// Errors ignored: the Fake writes these values itself in a controlled
		// format, so a parse failure would indicate a bug in this same file.
		tokens, _ = strconv.ParseFloat(t, 64)
		lastRefill, _ = strconv.ParseInt(h["last_refill"], 10, 64)
	}
	elapsed := float64(nowMs-lastRefill) / 1000.0
	if elapsed > 0 {
		tokens += elapsed * refillRate
		if tokens > float64(capacity) {
			tokens = float64(capacity)
		}
	}
	allowed := false
	if tokens >= float64(requested) {
		tokens -= float64(requested)
		allowed = true
	}
	h["tokens"] = strconv.FormatFloat(tokens, 'f', -1, 64)
	h["last_refill"] = strconv.FormatInt(nowMs, 10)

	var resetMs int64
	if tokens < float64(capacity) && refillRate > 0 {
		resetMs = int64((float64(capacity) - tokens) / refillRate * 1000)
	}
	return ScriptResult{Allowed: allowed, Count: int64(tokens), ResetMs: resetMs, IsTokens: true}, nil
}

func (f *Fake) EvalSlidingWindow(ctx context.Context, key string, windowMs, limit, nowMs int64, member string, ttlSec int64) (ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shouldFail() {
		return ScriptResult{}, ErrRedisUnavailable
	}

	cutoff := nowMs - windowMs
	entries := f.sortedSets[key]
	// trim
	trimmed := entries[:0]
	for _, e := range entries {
		if e.score >= cutoff {
			trimmed = append(trimmed, e)
		}
	}
	count := int64(len(trimmed))
	allowed := false
	if count < limit {
		trimmed = append(trimmed, sortedEntry{score: nowMs, member: member})
		sort.Slice(trimmed, func(i, j int) bool { return trimmed[i].score < trimmed[j].score })
		count++
		allowed = true
	}
	f.sortedSets[key] = trimmed

	resetAt := nowMs + windowMs
	if len(trimmed) > 0 {
		resetAt = trimmed[0].score + windowMs
	}
	return ScriptResult{Allowed: allowed, Count: count, ResetMs: resetAt}, nil
}

func (f *Fake) EvalFixedWindow(ctx context.Context, key string, limit, windowSec, nowMs int64) (ScriptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shouldFail() {
		return ScriptResult{}, ErrRedisUnavailable
	}

	c, ok := f.counters[key]
	now := time.UnixMilli(nowMs)
	if !ok || c.expires.Before(now) {
		c = &counter{value: 0, expires: now.Add(time.Duration(windowSec) * time.Second)}
		f.counters[key] = c
	}
	c.value++
	allowed := true
	count := c.value
	if c.value > limit {
		c.value--
		count = limit + 1
		allowed = false
	}
	resetAt := c.expires.UnixMilli()
	return ScriptResult{Allowed: allowed, Count: count, ResetMs: resetAt}, nil
}

func (f *Fake) Get(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.counters[key]; ok {
		return strconv.FormatInt(c.value, 10), nil
	}
	return "", nil
}

func (f *Fake) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.hashes[key]
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out, nil
}

func (f *Fake) ZCard(ctx context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.sortedSets[key])), nil
}

func (f *Fake) Del(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hashes, key)
	delete(f.sortedSets, key)
	delete(f.counters, key)
	return nil
}

func (f *Fake) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failForever {
		return errors.New("fake redis unavailable")
	}
	return nil
}

func (f *Fake) Close() error { return nil }
