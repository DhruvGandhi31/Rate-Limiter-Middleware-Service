// Package integration runs end-to-end tests against a real Redis.
//
// Test container resolution order:
//  1. If REDIS_ADDR is set, use that (CI uses this; faster than a container).
//  2. Otherwise spin up a Redis container via testcontainers-go.
//  3. If neither works, skip — never silently false-positive.
package integration

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// containerOnce gives every test in the package a single shared container so
// the suite isn't paying ~3s of Docker startup per test.
var (
	containerOnce sync.Once
	containerAddr string
	containerErr  error
)

func redisAddr(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		return v
	}
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		c, err := tcredis.Run(ctx, "redis:7-alpine")
		if err != nil {
			containerErr = err
			return
		}
		uri, err := c.ConnectionString(ctx)
		if err != nil {
			containerErr = err
			return
		}
		// testcontainers returns redis://host:port — strip the scheme.
		containerAddr = strings.TrimPrefix(uri, "redis://")
		testcontainers.CleanupContainer(t, c)
	})
	if containerErr != nil {
		t.Skipf("testcontainers redis unavailable: %v", containerErr)
	}
	return containerAddr
}

func newRedis(t *testing.T) *store.RedisStore {
	t.Helper()
	addr := redisAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := store.NewRedis(ctx, config.RedisConfig{Addr: addr, PoolSize: 20})
	if err != nil {
		t.Fatalf("connect to redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func uniqKey(t *testing.T) string {
	return strings.ReplaceAll(t.Name(), "/", "_") + "-" + time.Now().Format("150405.000000000")
}
