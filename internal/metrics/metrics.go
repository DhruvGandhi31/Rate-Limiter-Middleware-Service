// Package metrics defines the Prometheus collectors used across the service.
// One package keeps every metric name/label-set in a single file, which is the
// only way to keep them consistent over time.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RequestsTotal counts every limiter decision. The decision label is
	// "allowed" | "rejected" | "error" so dashboards can compute the
	// rejection-rate and error-rate independently.
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_requests_total",
		Help: "Total rate-limit decisions made, labeled by rule and outcome.",
	}, []string{"rule", "algorithm", "decision"})

	// DecisionDuration is the wall-clock time spent inside Limiter.Allow,
	// which is dominated by the Redis EVAL round-trip.
	DecisionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ratelimit_decision_duration_seconds",
		Help:    "Latency of a rate-limit decision (includes Redis round-trip).",
		Buckets: prometheus.ExponentialBucketsRange(0.0001, 1.0, 12), // 0.1ms → 1s
	}, []string{"rule", "algorithm"})

	// RedisErrors is the operational alert signal: if this is non-zero, the
	// service is in degraded mode and your fail_mode policy is in effect.
	RedisErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_redis_errors_total",
		Help: "Redis errors encountered during a limiter decision, by rule.",
	}, []string{"rule"})

	// HTTPRequestsTotal tracks the HTTP API surface independently of limiter
	// decisions (so /health and /metrics show up too).
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_http_requests_total",
		Help: "HTTP requests served, labeled by route and status class.",
	}, []string{"route", "status"})

	// HTTPRequestDuration captures HTTP-layer latency including JSON
	// encode/decode, distinct from DecisionDuration which is limiter-only.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ratelimit_http_request_duration_seconds",
		Help:    "End-to-end HTTP request latency.",
		Buckets: prometheus.ExponentialBucketsRange(0.0001, 2.0, 14),
	}, []string{"route"})
)
