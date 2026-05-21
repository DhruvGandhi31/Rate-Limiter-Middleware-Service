package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
)

//go:embed scripts/token_bucket.lua
var tokenBucketScript string

//go:embed scripts/sliding_window.lua
var slidingWindowScript string

//go:embed scripts/fixed_window.lua
var fixedWindowScript string

// Store is the minimal interface the limiter algorithms depend on. A real
// Redis client satisfies it; tests can provide a fake.
type Store interface {
	EvalTokenBucket(ctx context.Context, key string, capacity int64, refillRate float64, nowMs, requested, ttlSec int64) (ScriptResult, error)
	EvalSlidingWindow(ctx context.Context, key string, windowMs, limit, nowMs int64, member string, ttlSec int64) (ScriptResult, error)
	EvalFixedWindow(ctx context.Context, key string, limit, windowSec, nowMs int64) (ScriptResult, error)
	Get(ctx context.Context, key string) (string, error)
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	ZCard(ctx context.Context, key string) (int64, error)
	Del(ctx context.Context, key string) error
	Ping(ctx context.Context) error
	Close() error
}

// ScriptResult is the normalized result of a limiter Lua script.
type ScriptResult struct {
	Allowed   bool
	Count     int64 // for fixed/sliding: requests in window. for token: remaining tokens.
	ResetMs   int64 // ms-since-epoch when the limit resets (sliding/fixed) or ms until refill (token)
	IsTokens  bool  // true when Count means "remaining tokens" rather than "consumed"
}

var ErrRedisUnavailable = errors.New("redis unavailable")

// RedisStore implements Store against a real Redis instance.
type RedisStore struct {
	client *redis.Client

	tokenBucketSHA   string
	slidingWindowSHA string
	fixedWindowSHA   string
}

func NewRedis(ctx context.Context, cfg config.RedisConfig) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		MaxRetries:   2,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	s := &RedisStore{client: client}
	if err := s.loadScripts(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *RedisStore) loadScripts(ctx context.Context) error {
	var err error
	if s.tokenBucketSHA, err = s.client.ScriptLoad(ctx, tokenBucketScript).Result(); err != nil {
		return fmt.Errorf("load token_bucket script: %w", err)
	}
	if s.slidingWindowSHA, err = s.client.ScriptLoad(ctx, slidingWindowScript).Result(); err != nil {
		return fmt.Errorf("load sliding_window script: %w", err)
	}
	if s.fixedWindowSHA, err = s.client.ScriptLoad(ctx, fixedWindowScript).Result(); err != nil {
		return fmt.Errorf("load fixed_window script: %w", err)
	}
	return nil
}

func (s *RedisStore) Close() error  { return s.client.Close() }
func (s *RedisStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

func (s *RedisStore) Del(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	v, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return v, err
}

func (s *RedisStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return s.client.HGetAll(ctx, key).Result()
}

func (s *RedisStore) ZCard(ctx context.Context, key string) (int64, error) {
	return s.client.ZCard(ctx, key).Result()
}

// evalCached runs a preloaded script by SHA, falling back to EVAL if Redis has
// dropped it (e.g. SCRIPT FLUSH or a fresh replica).
func (s *RedisStore) evalCached(ctx context.Context, sha, src string, keys []string, args ...interface{}) (interface{}, error) {
	res, err := s.client.EvalSha(ctx, sha, keys, args...).Result()
	if err == nil {
		return res, nil
	}
	// NOSCRIPT means we need to reload.
	if isNoScript(err) {
		res, err = s.client.Eval(ctx, src, keys, args...).Result()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRedisUnavailable, err)
	}
	return res, nil
}

func isNoScript(err error) bool {
	return err != nil && len(err.Error()) >= 8 && err.Error()[:8] == "NOSCRIPT"
}

func (s *RedisStore) EvalTokenBucket(ctx context.Context, key string, capacity int64, refillRate float64, nowMs, requested, ttlSec int64) (ScriptResult, error) {
	raw, err := s.evalCached(ctx, s.tokenBucketSHA, tokenBucketScript, []string{key},
		capacity, refillRate, nowMs, requested, ttlSec)
	if err != nil {
		return ScriptResult{}, err
	}
	return parseResult(raw, true)
}

func (s *RedisStore) EvalSlidingWindow(ctx context.Context, key string, windowMs, limit, nowMs int64, member string, ttlSec int64) (ScriptResult, error) {
	raw, err := s.evalCached(ctx, s.slidingWindowSHA, slidingWindowScript, []string{key},
		windowMs, limit, nowMs, member, ttlSec)
	if err != nil {
		return ScriptResult{}, err
	}
	return parseResult(raw, false)
}

func (s *RedisStore) EvalFixedWindow(ctx context.Context, key string, limit, windowSec, nowMs int64) (ScriptResult, error) {
	raw, err := s.evalCached(ctx, s.fixedWindowSHA, fixedWindowScript, []string{key},
		limit, windowSec, nowMs)
	if err != nil {
		return ScriptResult{}, err
	}
	return parseResult(raw, false)
}

func parseResult(raw interface{}, isTokens bool) (ScriptResult, error) {
	arr, ok := raw.([]interface{})
	if !ok || len(arr) < 3 {
		return ScriptResult{}, fmt.Errorf("unexpected script result: %v", raw)
	}
	allowed, _ := arr[0].(int64)
	count, _ := arr[1].(int64)
	reset, _ := arr[2].(int64)
	return ScriptResult{
		Allowed:  allowed == 1,
		Count:    count,
		ResetMs:  reset,
		IsTokens: isTokens,
	}, nil
}
