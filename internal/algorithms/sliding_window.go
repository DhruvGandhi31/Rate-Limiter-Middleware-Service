package algorithms

import (
	"context"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// SlidingWindow keeps a per-key sorted set of request timestamps and rejects
// once Limit entries fall inside Window. Accurate but O(N) memory per client.
type SlidingWindow struct {
	Store store.Store
}

func (s *SlidingWindow) Allow(ctx context.Context, key string, rule config.Rule) (Decision, error) {
	now := nowMs()
	windowMs := rule.Window.Milliseconds()
	if windowMs < 1 {
		windowMs = 1
	}
	ttl := int64(rule.Window.Seconds()) + 1
	if ttl < 2 {
		ttl = 2
	}

	res, err := s.Store.EvalSlidingWindow(ctx, key, windowMs, rule.Limit, now, uniqueMember(now), ttl)
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
