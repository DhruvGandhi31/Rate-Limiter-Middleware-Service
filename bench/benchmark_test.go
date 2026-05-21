// Package bench measures throughput and latency for the limiter algorithms
// against the in-memory Fake store (for a pure-algorithm baseline) and the
// real Redis store when REDIS_ADDR is reachable (for end-to-end numbers).
//
// Run with:
//
//	go test -bench=. -benchmem ./bench/...
//	REDIS_ADDR=localhost:6379 go test -bench=Redis -benchmem ./bench/...
package bench

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

func benchAlgo(b *testing.B, l algorithms.Limiter, rule config.Rule) {
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = l.Allow(ctx, "bench-key", rule)
		}
	})
}

func BenchmarkFake_TokenBucket(b *testing.B) {
	s := store.NewFake()
	l := &algorithms.TokenBucket{Store: s}
	benchAlgo(b, l, config.Rule{Algorithm: config.TokenBucket, Limit: 1_000_000, RefillRate: 1_000_000})
}

func BenchmarkFake_FixedWindow(b *testing.B) {
	s := store.NewFake()
	l := &algorithms.FixedWindow{Store: s}
	benchAlgo(b, l, config.Rule{Name: "b", Algorithm: config.FixedWindow, Limit: 1_000_000, Window: time.Hour})
}

func BenchmarkFake_SlidingWindow(b *testing.B) {
	s := store.NewFake()
	l := &algorithms.SlidingWindow{Store: s}
	benchAlgo(b, l, config.Rule{Algorithm: config.SlidingWindow, Limit: 1_000_000, Window: time.Hour})
}

func newRedisOrSkip(tb testing.TB) store.Store {
	tb.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := store.NewRedis(ctx, config.RedisConfig{Addr: addr, PoolSize: 100})
	if err != nil {
		tb.Skipf("redis not available: %v", err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	return s
}

func BenchmarkRedis_TokenBucket(b *testing.B) {
	s := newRedisOrSkip(b)
	l := &algorithms.TokenBucket{Store: s}
	benchAlgo(b, l, config.Rule{Algorithm: config.TokenBucket, Limit: 1_000_000, RefillRate: 1_000_000})
}

func BenchmarkRedis_FixedWindow(b *testing.B) {
	s := newRedisOrSkip(b)
	l := &algorithms.FixedWindow{Store: s}
	benchAlgo(b, l, config.Rule{Name: "b", Algorithm: config.FixedWindow, Limit: 1_000_000, Window: time.Hour})
}

func BenchmarkRedis_SlidingWindow(b *testing.B) {
	s := newRedisOrSkip(b)
	l := &algorithms.SlidingWindow{Store: s}
	benchAlgo(b, l, config.Rule{Algorithm: config.SlidingWindow, Limit: 1_000_000, Window: time.Hour})
}
