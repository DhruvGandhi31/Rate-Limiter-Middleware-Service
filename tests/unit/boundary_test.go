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

// TestFixedWindow_BoundaryBurstVulnerability documents the known weakness of
// the fixed window algorithm: a client can fire 2x the limit by straddling the
// window boundary. We *assert the vulnerability is observable*, which is what
// the spec asks for ("assert the burst vulnerability is observable and
// documented").
func TestFixedWindow_BoundaryBurstVulnerability(t *testing.T) {
	s := store.NewFake()
	fw := &algorithms.FixedWindow{Store: s}
	rule := config.Rule{Name: "fw", Algorithm: config.FixedWindow, Limit: 5, Window: time.Second}

	// Wait for the start of a fresh window (within ~50ms of the boundary).
	for {
		now := time.Now()
		ms := now.UnixMilli() % rule.Window.Milliseconds()
		if ms > 850 { // we're in the back third — fire here
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Fire limit requests at the END of window N.
	for i := 0; i < 5; i++ {
		d, err := fw.Allow(context.Background(), "boundary", rule)
		require.NoError(t, err)
		require.True(t, d.Allowed, "request %d in window N should be allowed", i)
	}

	// Wait until we cross into window N+1.
	for {
		now := time.Now()
		ms := now.UnixMilli() % rule.Window.Milliseconds()
		if ms < 100 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Fire limit requests at the START of window N+1.
	for i := 0; i < 5; i++ {
		d, err := fw.Allow(context.Background(), "boundary", rule)
		require.NoError(t, err)
		assert.True(t, d.Allowed, "request %d in window N+1 should be allowed (this is the vulnerability)", i)
	}

	// 2x limit succeeded inside a sub-second span — that's the boundary burst.
}
