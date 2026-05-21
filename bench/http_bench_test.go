package bench

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/dhruvgandhi/rate-limiter/api"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
)

// quietLogger discards everything below error — benchmarks shouldn't pay for log I/O.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// buildHTTPHandler wires the full stack (handler → middleware → algorithm →
// Redis) so the benchmark exercises everything except the kernel TCP socket.
//
// Why no real TCP? Under sustained parallel benchmark fire on Windows we
// burn through ephemeral ports faster than the OS recycles them, masking
// the actual handler latency. Calling ServeHTTP directly with a recorder
// measures the same code path without that noise.
func buildHTTPHandler(b testing.TB, algo config.Algorithm) http.Handler {
	s := newRedisOrSkip(b)
	cfg := &config.Config{
		FailMode: config.FailClosed,
		Rules: []config.Rule{{
			Name: "bench", Identifier: "api_key", Algorithm: algo,
			Limit: 10_000_000, RefillRate: 10_000_000,
			Window: time.Hour,
		}},
	}
	l, err := middleware.New(cfg, s)
	if err != nil {
		b.Fatal(err)
	}
	return api.NewHandler(l, s, quietLogger(), nil).Routes()
}

// BenchmarkHTTP_TokenBucket exercises the full HTTP stack (router, JSON,
// middleware, atomic Redis EVAL) without an actual TCP socket.
//
//	REDIS_ADDR=localhost:6379 go test -bench=HTTP -benchtime=5s -benchmem ./bench/...
func BenchmarkHTTP_TokenBucket(b *testing.B) {
	h := buildHTTPHandler(b, config.TokenBucket)
	body, _ := json.Marshal(map[string]string{"rule": "bench", "identifier": "load"})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("POST", "/check", bytes.NewReader(body))
			req.Header.Set("content-type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
		}
	})
}

func BenchmarkHTTP_FixedWindow(b *testing.B) {
	h := buildHTTPHandler(b, config.FixedWindow)
	body, _ := json.Marshal(map[string]string{"rule": "bench", "identifier": "load"})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("POST", "/check", bytes.NewReader(body))
			req.Header.Set("content-type", "application/json")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}

func BenchmarkHTTP_SlidingWindow(b *testing.B) {
	h := buildHTTPHandler(b, config.SlidingWindow)
	body, _ := json.Marshal(map[string]string{"rule": "bench", "identifier": "load"})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("POST", "/check", bytes.NewReader(body))
			req.Header.Set("content-type", "application/json")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}

// TestHTTPLatency_TokenBucket reports operational percentiles using a real TCP
// httptest.Server. Concurrency is intentionally modest so Windows doesn't run
// out of ephemeral ports; bump it if you're on Linux.
//
//	REDIS_ADDR=localhost:6379 go test -run HTTPLatency -v ./bench/...
func TestHTTPLatency_TokenBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("latency probe is long-running; run without -short")
	}
	s := newRedisOrSkip(t)
	cfg := &config.Config{
		FailMode: config.FailClosed,
		Rules: []config.Rule{{
			Name: "lat", Identifier: "api_key", Algorithm: config.TokenBucket,
			Limit: 10_000_000, RefillRate: 10_000_000,
		}},
	}
	l, err := middleware.New(cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewHandler(l, s, quietLogger(), nil).Routes())
	defer srv.Close()

	const (
		concurrency = 20
		perWorker   = 500
	)
	body, _ := json.Marshal(map[string]string{"rule": "lat", "identifier": "lat"})

	tr := &http.Transport{
		MaxIdleConns:        concurrency * 2,
		MaxIdleConnsPerHost: concurrency * 2,
		MaxConnsPerHost:     concurrency * 2,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	results := make(chan time.Duration, concurrency*perWorker)
	start := time.Now()
	for i := 0; i < concurrency; i++ {
		go func() {
			for j := 0; j < perWorker; j++ {
				t0 := time.Now()
				req, _ := http.NewRequest("POST", srv.URL+"/check", bytes.NewReader(body))
				req.Header.Set("content-type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					results <- 0
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				results <- time.Since(t0)
			}
		}()
	}

	durs := make([]time.Duration, 0, concurrency*perWorker)
	for i := 0; i < concurrency*perWorker; i++ {
		durs = append(durs, <-results)
	}
	wall := time.Since(start)
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })

	pct := func(p float64) time.Duration { return durs[int(float64(len(durs))*p)] }
	t.Logf("HTTP layer latency (concurrency=%d, requests=%d):", concurrency, len(durs))
	t.Logf("  wall:       %v", wall)
	t.Logf("  throughput: %.0f req/s", float64(len(durs))/wall.Seconds())
	t.Logf("  p50:        %v", pct(0.50))
	t.Logf("  p95:        %v", pct(0.95))
	t.Logf("  p99:        %v", pct(0.99))
	t.Logf("  p99.9:      %v", pct(0.999))
	t.Logf("  max:        %v", durs[len(durs)-1])
}
