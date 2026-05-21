package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
)

func TestIntegration_TokenBucket_Burst(t *testing.T) {
	s := newRedis(t)
	tb := &algorithms.TokenBucket{Store: s}
	rule := config.Rule{Algorithm: config.TokenBucket, Limit: 10, RefillRate: 0.0001}
	key := uniqKey(t)
	defer s.Del(context.Background(), key)

	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := tb.Allow(context.Background(), key, rule)
			require.NoError(t, err)
			if d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(10), atomic.LoadInt64(&allowed))
}

func TestIntegration_SlidingWindow_AgesOut(t *testing.T) {
	s := newRedis(t)
	sw := &algorithms.SlidingWindow{Store: s}
	rule := config.Rule{Algorithm: config.SlidingWindow, Limit: 3, Window: 300 * time.Millisecond}
	key := uniqKey(t)
	defer s.Del(context.Background(), key)

	for i := 0; i < 3; i++ {
		d, err := sw.Allow(context.Background(), key, rule)
		require.NoError(t, err)
		require.True(t, d.Allowed)
	}
	d, err := sw.Allow(context.Background(), key, rule)
	require.NoError(t, err)
	require.False(t, d.Allowed)

	time.Sleep(350 * time.Millisecond)
	d, err = sw.Allow(context.Background(), key, rule)
	require.NoError(t, err)
	assert.True(t, d.Allowed)
}

func TestIntegration_FixedWindow_Resets(t *testing.T) {
	s := newRedis(t)
	fw := &algorithms.FixedWindow{Store: s}
	rule := config.Rule{Name: "fw", Algorithm: config.FixedWindow, Limit: 5, Window: time.Second}
	key := uniqKey(t)
	defer s.Del(context.Background(), key)

	allowed := 0
	for i := 0; i < 10; i++ {
		d, err := fw.Allow(context.Background(), key, rule)
		require.NoError(t, err)
		if d.Allowed {
			allowed++
		}
	}
	assert.Equal(t, 5, allowed)
}

func TestIntegration_EndToEndHTTP(t *testing.T) {
	s := newRedis(t)
	cfg := &config.Config{
		FailMode: config.FailClosed,
		Rules: []config.Rule{{
			Name: "e2e", Identifier: "ip", Algorithm: config.TokenBucket,
			Limit: 3, RefillRate: 0.001,
		}},
	}
	l, err := middleware.New(cfg, s)
	require.NoError(t, err)

	defer func() {
		_ = s.Del(context.Background(), algorithms.KeyFor(config.TokenBucket, "ip:127.0.0.1", "e2e"))
	}()

	target := httptest.NewServer(l.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer target.Close()

	allowed := 0
	rejected := 0
	for i := 0; i < 10; i++ {
		resp, err := http.Get(target.URL + "/x")
		require.NoError(t, err)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			allowed++
		} else if resp.StatusCode == http.StatusTooManyRequests {
			rejected++
			require.NotEmpty(t, resp.Header.Get("Retry-After"))
			require.NotEmpty(t, resp.Header.Get("X-RateLimit-Limit"))
		}
	}
	assert.Equal(t, 3, allowed)
	assert.Equal(t, 7, rejected)
}
