package algorithms

import (
	"context"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// TokenBucket allows bursts up to rule.Limit and refills at rule.RefillRate
// tokens/second. State is a Redis hash {tokens, last_refill}.
type TokenBucket struct {
	Store store.Store
}

func (t *TokenBucket) Allow(ctx context.Context, key string, rule config.Rule) (Decision, error) {
	now := nowMs()
	// TTL: long enough to outlive a full-refill window so idle buckets eventually expire.
	ttl := int64(60)
	if rule.RefillRate > 0 {
		full := int64(float64(rule.Limit) / rule.RefillRate)
		if full > ttl {
			ttl = full * 2
		}
	}

	res, err := t.Store.EvalTokenBucket(ctx, key, rule.Limit, rule.RefillRate, now, 1, ttl)
	if err != nil {
		return Decision{}, err
	}

	d := Decision{
		Allowed:   res.Allowed,
		Limit:     rule.Limit,
		Remaining: res.Count,
		ResetAt:   time.UnixMilli(now + res.ResetMs),
	}
	if !res.Allowed && rule.RefillRate > 0 {
		// Time until at least one token is available.
		d.RetryAfter = time.Duration(float64(time.Second) / rule.RefillRate)
	}
	return d, nil
}
