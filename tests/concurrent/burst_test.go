package concurrent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// TestTokenBucket_50ConcurrentGoroutines is the marquee correctness test from the spec:
// fire 50 goroutines at the same key and assert exactly the limit succeeds.
func TestTokenBucket_50ConcurrentGoroutines(t *testing.T) {
	s := store.NewFake()
	tb := &algorithms.TokenBucket{Store: s}
	const limit = 20
	rule := config.Rule{Algorithm: config.TokenBucket, Limit: limit, RefillRate: 0.000001}

	var allowed int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := tb.Allow(context.Background(), "k", rule)
			require.NoError(t, err)
			if d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(limit), atomic.LoadInt64(&allowed),
		"exactly limit requests must succeed under concurrent burst")
}

func TestFixedWindow_50ConcurrentGoroutines(t *testing.T) {
	s := store.NewFake()
	fw := &algorithms.FixedWindow{Store: s}
	const limit = 15
	rule := config.Rule{Name: "fw", Algorithm: config.FixedWindow, Limit: limit, Window: 5 * time.Second}

	var allowed int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := fw.Allow(context.Background(), "k", rule)
			require.NoError(t, err)
			if d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(limit), atomic.LoadInt64(&allowed))
}

func TestSlidingWindow_50ConcurrentGoroutines(t *testing.T) {
	s := store.NewFake()
	sw := &algorithms.SlidingWindow{Store: s}
	const limit = 25
	rule := config.Rule{Algorithm: config.SlidingWindow, Limit: limit, Window: 5 * time.Second}

	var allowed int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := sw.Allow(context.Background(), "k", rule)
			require.NoError(t, err)
			if d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(limit), atomic.LoadInt64(&allowed))
}
