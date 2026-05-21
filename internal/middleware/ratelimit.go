// Package middleware ties config rules to algorithm instances and produces the
// HTTP middleware that enforces them.
package middleware

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/metrics"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// CompiledRule is a config.Rule pre-bound to its limiter implementation and
// (optionally) a compiled match regex, so the request path is allocation-free.
type CompiledRule struct {
	config.Rule
	Limiter algorithms.Limiter
	Match   *regexp.Regexp // nil = match all
}

// Limiter is the public entry point used by both the HTTP middleware and the
// /check API handler. It picks the first matching rule for a request.
type Limiter struct {
	Rules    []CompiledRule
	FailMode config.FailMode
}

func New(cfg *config.Config, s store.Store) (*Limiter, error) {
	reg := algorithms.NewRegistry(s)
	rules := make([]CompiledRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		impl, ok := reg.For(r.Algorithm)
		if !ok {
			return nil, fmt.Errorf("rule %q: unsupported algorithm %s", r.Name, r.Algorithm)
		}
		cr := CompiledRule{Rule: r, Limiter: impl}
		if r.Match != "" {
			re, err := regexp.Compile(r.Match)
			if err != nil {
				return nil, fmt.Errorf("rule %q: bad match regex: %w", r.Name, err)
			}
			cr.Match = re
		}
		rules = append(rules, cr)
	}
	return &Limiter{Rules: rules, FailMode: cfg.FailMode}, nil
}

// IdentifierFor extracts the bucket key for a request based on a rule's
// identifier strategy.
func IdentifierFor(r *http.Request, kind string) string {
	switch kind {
	case "api_key":
		if k := r.Header.Get("X-API-Key"); k != "" {
			return "api:" + k
		}
		if k := r.Header.Get("Authorization"); k != "" {
			return "api:" + k
		}
		return "api:anonymous"
	case "ip":
		return "ip:" + clientIP(r)
	case "endpoint":
		return "ep:" + r.URL.Path
	default:
		return "default"
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Match returns the first rule whose Identifier strategy and Match regex apply
// to this request, plus the per-request bucket key.
func (l *Limiter) Match(r *http.Request) (CompiledRule, string, bool) {
	for _, rule := range l.Rules {
		if rule.Match != nil && !rule.Match.MatchString(r.URL.Path) {
			continue
		}
		id := IdentifierFor(r, rule.Identifier)
		key := algorithms.KeyFor(rule.Algorithm, id, rule.Name)
		return rule, key, true
	}
	return CompiledRule{}, "", false
}

// Allow applies the matched rule (if any). When no rule matches it returns
// "allowed" with a zero Decision, leaving the caller to forward the request.
func (l *Limiter) Allow(ctx context.Context, r *http.Request) (algorithms.Decision, bool, error) {
	rule, key, ok := l.Match(r)
	if !ok {
		return algorithms.Decision{Allowed: true}, false, nil
	}
	return l.evaluate(ctx, rule, key)
}

// AllowKey is the API-driven path: caller passes an explicit key + rule.
func (l *Limiter) AllowKey(ctx context.Context, ruleName, identifier string) (algorithms.Decision, error) {
	for _, rule := range l.Rules {
		if rule.Name != ruleName {
			continue
		}
		key := algorithms.KeyFor(rule.Algorithm, identifier, rule.Name)
		d, _, err := l.evaluate(ctx, rule, key)
		return d, err
	}
	return algorithms.Decision{}, fmt.Errorf("rule not found: %s", ruleName)
}

// evaluate is the single instrumented call site for limiter decisions. Every
// allow/reject/error flows through here, so the metrics are a single source of
// truth for "what did the limiter actually do."
func (l *Limiter) evaluate(ctx context.Context, rule CompiledRule, key string) (algorithms.Decision, bool, error) {
	start := time.Now()
	d, err := rule.Limiter.Allow(ctx, key, rule.Rule)
	metrics.DecisionDuration.WithLabelValues(rule.Name, string(rule.Algorithm)).Observe(time.Since(start).Seconds())

	if err != nil {
		if errors.Is(err, store.ErrRedisUnavailable) {
			metrics.RedisErrors.WithLabelValues(rule.Name).Inc()
			d := algorithms.Decision{Allowed: l.FailMode == config.FailOpen, Limit: rule.Limit}
			labelForFailMode := "rejected"
			if d.Allowed {
				labelForFailMode = "allowed"
			}
			metrics.RequestsTotal.WithLabelValues(rule.Name, string(rule.Algorithm), labelForFailMode).Inc()
			return d, true, err
		}
		metrics.RequestsTotal.WithLabelValues(rule.Name, string(rule.Algorithm), "error").Inc()
		return algorithms.Decision{}, true, err
	}

	decision := "rejected"
	if d.Allowed {
		decision = "allowed"
	}
	metrics.RequestsTotal.WithLabelValues(rule.Name, string(rule.Algorithm), decision).Inc()
	return d, true, nil
}

// RuleByName is exposed so admin handlers can resolve a rule for /reset.
func (l *Limiter) RuleByName(name string) (CompiledRule, bool) {
	for _, r := range l.Rules {
		if r.Name == name {
			return r, true
		}
	}
	return CompiledRule{}, false
}

// HTTPMiddleware enforces rate limits in front of an inner handler. Rejected
// requests respond with HTTP 429 and Stripe-style headers.
func (l *Limiter) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, applied, err := l.Allow(r.Context(), r)
		if applied {
			WriteHeaders(w, d)
		}
		if err != nil && !errors.Is(err, store.ErrRedisUnavailable) {
			http.Error(w, "rate limiter error", http.StatusInternalServerError)
			return
		}
		if applied && !d.Allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Round(time.Second).Seconds())))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteHeaders sets the rate-limit response headers Stripe and most public
// APIs use. Safe to call before WriteHeader.
func WriteHeaders(w http.ResponseWriter, d algorithms.Decision) {
	if d.Limit > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(d.Limit, 10))
	}
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(d.Remaining, 10))
	if !d.ResetAt.IsZero() {
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(d.ResetAt.Unix(), 10))
	}
}
