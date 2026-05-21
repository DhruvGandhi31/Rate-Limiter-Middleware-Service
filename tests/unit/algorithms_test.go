package unit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

func TestTokenBucket_AllowsUpToCapacityThenRejects(t *testing.T) {
	s := store.NewFake()
	tb := &algorithms.TokenBucket{Store: s}
	rule := config.Rule{
		Name: "t", Algorithm: config.TokenBucket,
		Limit: 5, RefillRate: 0.0001, // effectively no refill in test window
	}

	for i := 0; i < 5; i++ {
		d, err := tb.Allow(context.Background(), "k", rule)
		require.NoError(t, err)
		assert.True(t, d.Allowed, "request %d should be allowed", i)
	}
	d, err := tb.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	assert.False(t, d.Allowed, "6th request should be rejected")
	assert.Equal(t, int64(0), d.Remaining)
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	s := store.NewFake()
	tb := &algorithms.TokenBucket{Store: s}
	rule := config.Rule{Algorithm: config.TokenBucket, Limit: 2, RefillRate: 100} // 100/sec

	// drain
	for i := 0; i < 2; i++ {
		d, err := tb.Allow(context.Background(), "k", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := tb.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)

	time.Sleep(50 * time.Millisecond) // ~5 tokens worth
	d, err = tb.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	assert.True(t, d.Allowed, "should be allowed after refill")
}

func TestFixedWindow_AllowsLimitPerWindow(t *testing.T) {
	s := store.NewFake()
	fw := &algorithms.FixedWindow{Store: s}
	rule := config.Rule{Name: "fw", Algorithm: config.FixedWindow, Limit: 3, Window: time.Second}

	for i := 0; i < 3; i++ {
		d, err := fw.Allow(context.Background(), "k", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := fw.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	assert.False(t, d.Allowed)
	assert.Equal(t, int64(3), d.Limit)
	assert.Greater(t, d.RetryAfter, time.Duration(0))
}

func TestSlidingWindow_RejectsOnceFullWithinWindow(t *testing.T) {
	s := store.NewFake()
	sw := &algorithms.SlidingWindow{Store: s}
	rule := config.Rule{Algorithm: config.SlidingWindow, Limit: 3, Window: time.Second}

	for i := 0; i < 3; i++ {
		d, err := sw.Allow(context.Background(), "k", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := sw.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	assert.False(t, d.Allowed)
}

func TestSlidingWindow_OldEntriesAgeOut(t *testing.T) {
	s := store.NewFake()
	sw := &algorithms.SlidingWindow{Store: s}
	rule := config.Rule{Algorithm: config.SlidingWindow, Limit: 2, Window: 100 * time.Millisecond}

	for i := 0; i < 2; i++ {
		d, err := sw.Allow(context.Background(), "k", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	time.Sleep(120 * time.Millisecond)
	d, err := sw.Allow(context.Background(), "k", rule)
	require.NoError(t, err)
	assert.True(t, d.Allowed, "old entries should have aged out")
}
