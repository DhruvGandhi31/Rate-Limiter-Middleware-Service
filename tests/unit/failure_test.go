package unit

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// TestRedisFailure_FailClosed verifies that with FailMode=closed, a Redis
// outage causes the limiter to reject requests rather than let everything through.
func TestRedisFailure_FailClosed(t *testing.T) {
	cfg := &config.Config{
		FailMode: config.FailClosed,
		Rules: []config.Rule{{
			Name: "r", Identifier: "ip", Algorithm: config.TokenBucket,
			Limit: 10, RefillRate: 5,
		}},
	}
	s := store.NewFake()
	s.FailForever()

	l, err := middleware.New(cfg, s)
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "1.2.3.4:1"
	d, applied, err := l.Allow(context.Background(), r)
	require.True(t, errors.Is(err, store.ErrRedisUnavailable))
	require.True(t, applied)
	assert.False(t, d.Allowed, "fail_mode=closed must reject when Redis is down")
}

// TestRedisFailure_FailOpen verifies the opposite: availability over correctness.
func TestRedisFailure_FailOpen(t *testing.T) {
	cfg := &config.Config{
		FailMode: config.FailOpen,
		Rules: []config.Rule{{
			Name: "r", Identifier: "ip", Algorithm: config.SlidingWindow,
			Limit: 10, Window: time.Second,
		}},
	}
	s := store.NewFake()
	s.FailForever()

	l, err := middleware.New(cfg, s)
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "1.2.3.4:1"
	d, applied, err := l.Allow(context.Background(), r)
	require.True(t, errors.Is(err, store.ErrRedisUnavailable))
	require.True(t, applied)
	assert.True(t, d.Allowed, "fail_mode=open must allow when Redis is down")
}
