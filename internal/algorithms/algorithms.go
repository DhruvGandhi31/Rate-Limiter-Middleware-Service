// Package algorithms provides pluggable rate-limiting algorithms backed by a
// shared Redis store. Each algorithm satisfies the Limiter interface so the
// middleware can select one per rule without branching.
package algorithms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// Decision is the algorithm-agnostic result returned to the middleware.
type Decision struct {
	Allowed    bool
	Limit      int64
	Remaining  int64
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Limiter is implemented by every algorithm. Allow is the only operation:
// atomically check the rule and consume capacity if available.
type Limiter interface {
	Allow(ctx context.Context, key string, rule config.Rule) (Decision, error)
}

// New returns the limiter matching a rule's configured algorithm.
func New(algo config.Algorithm, s store.Store) (Limiter, error) {
	switch algo {
	case config.TokenBucket:
		return &TokenBucket{Store: s}, nil
	case config.FixedWindow:
		return &FixedWindow{Store: s}, nil
	case config.SlidingWindow:
		return &SlidingWindow{Store: s}, nil
	default:
		return nil, fmt.Errorf("unknown algorithm: %s", algo)
	}
}

// Registry wires every algorithm once at startup so the middleware never
// constructs limiters on the hot path.
type Registry struct {
	limiters map[config.Algorithm]Limiter
}

func NewRegistry(s store.Store) *Registry {
	return &Registry{
		limiters: map[config.Algorithm]Limiter{
			config.TokenBucket:   &TokenBucket{Store: s},
			config.FixedWindow:   &FixedWindow{Store: s},
			config.SlidingWindow: &SlidingWindow{Store: s},
		},
	}
}

func (r *Registry) For(algo config.Algorithm) (Limiter, bool) {
	l, ok := r.limiters[algo]
	return l, ok
}

// KeyFor builds a namespaced Redis key. Format intentionally matches the spec
// so operators can inspect state with `redis-cli KEYS ratelimit:*`.
func KeyFor(algo config.Algorithm, identifier, window string) string {
	if window == "" {
		return fmt.Sprintf("ratelimit:%s:%s", algo, identifier)
	}
	return fmt.Sprintf("ratelimit:%s:%s:%s", algo, identifier, window)
}

func nowMs() int64 { return time.Now().UnixMilli() }

func uniqueMember(now int64) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", now, hex.EncodeToString(b[:]))
}
