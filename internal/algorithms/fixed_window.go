package algorithms

import (
	"context"
	"fmt"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// FixedWindow groups requests into wall-clock buckets of rule.Window. Cheap
// and exact within a window, but a client can burst 2x at a window boundary.
type FixedWindow struct {
	Store store.Store
}

func (f *FixedWindow) Allow(ctx context.Context, key string, rule config.Rule) (Decision, error) {
	now := nowMs()
	windowSec := int64(rule.Window.Seconds())
	if windowSec < 1 {
		windowSec = 1
	}
	// Embed the window index in the key so a new window starts atomically when time crosses it.
	bucket := now / int64(rule.Window/time.Millisecond)
	windowKey := fmt.Sprintf("%s:%d", key, bucket)

	res, err := f.Store.EvalFixedWindow(ctx, windowKey, rule.Limit, windowSec, now)
	if err != nil {
		return Decision{}, err
	}

	remaining := rule.Limit - res.Count
	if remaining < 0 {
		remaining = 0
	}
	d := Decision{
		Allowed:   res.Allowed,
		Limit:     rule.Limit,
		Remaining: remaining,
		ResetAt:   time.UnixMilli(res.ResetMs),
	}
	if !res.Allowed {
		d.RetryAfter = time.Until(d.ResetAt)
		if d.RetryAfter < 0 {
			d.RetryAfter = 0
		}
	}
	return d, nil
}
