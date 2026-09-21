// Package api exposes the rate-limiter as an HTTP service. Clients call /check
// to consume a token, /status to inspect state, /reset for admin overrides, and
// /health for orchestration probes.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dhruvgandhi/rate-limiter/internal/algorithms"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/metrics"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

type Handler struct {
	Limiter   *middleware.Limiter
	Store     store.Store
	Logger    *slog.Logger
	AdminAuth AdminAuth // nil = admin endpoints open (dev only)
}

// AdminAuth gates admin endpoints (/reset). nil means no auth, suitable for
// local dev but never production — main wires a real auth in when configured.
type AdminAuth interface {
	Authorize(r *http.Request) bool
}

// BearerTokenAuth is a constant-time-compared shared-secret check. Cheap, no
// external dependency, fine for an internal admin surface. For multi-tenant
// production swap in OIDC / mTLS.
type BearerTokenAuth struct {
	Token string
}

func (b *BearerTokenAuth) Authorize(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	got := h[len(prefix):]
	// constant-time compare to avoid leaking length via timing
	if len(got) != len(b.Token) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ b.Token[i]
	}
	return diff == 0
}

func NewHandler(l *middleware.Limiter, s store.Store, log *slog.Logger, auth AdminAuth) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{Limiter: l, Store: s, Logger: log, AdminAuth: auth}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(h.observe)
	r.Get("/health", h.health)
	r.Handle("/metrics", promhttp.Handler())
	r.Post("/check", h.check)
	r.Get("/status/{key}", h.status)
	r.Group(func(r chi.Router) {
		r.Use(h.requireAdmin)
		r.Post("/reset/{key}", h.reset)
	})
	return r
}

// observe records HTTP metrics and a structured access log line for every
// request. Wrapping the response writer is the only way to learn the status
// code after the handler returns.
func (h *Handler) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = r.URL.Path
		}
		dur := time.Since(start)
		metrics.HTTPRequestDuration.WithLabelValues(route).Observe(dur.Seconds())
		metrics.HTTPRequestsTotal.WithLabelValues(route, statusClass(ww.status)).Inc()

		// Skip /metrics scrape spam at info level — debug only.
		if route == "/metrics" {
			h.Logger.Debug("http", "method", r.Method, "route", route, "status", ww.status, "duration_ms", dur.Milliseconds())
			return
		}
		h.Logger.Info("http", "method", r.Method, "route", route, "status", ww.status, "duration_ms", dur.Milliseconds())
	})
}

// requireAdmin rejects requests that don't satisfy the configured admin auth.
// When AdminAuth is nil (dev mode) it logs a loud warning so it's obvious in
// startup logs and access logs that the admin surface is open.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.AdminAuth == nil {
			h.Logger.Warn("admin endpoint hit with no auth configured", "route", r.URL.Path, "remote", r.RemoteAddr)
			next.ServeHTTP(w, r)
			return
		}
		if !h.AdminAuth.Authorize(r) {
			h.Logger.Warn("admin auth failed", "route", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

type checkRequest struct {
	Rule       string `json:"rule"`
	Identifier string `json:"identifier"`
}

type checkResponse struct {
	Allowed    bool   `json:"allowed"`
	Limit      int64  `json:"limit"`
	Remaining  int64  `json:"remaining"`
	ResetAt    int64  `json:"reset_at"`
	RetryAfter int64  `json:"retry_after_seconds,omitempty"`
	Rule       string `json:"rule"`
}

func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Identifier == "" {
		http.Error(w, "identifier is required", http.StatusBadRequest)
		return
	}
	if req.Rule == "" {
		req.Rule = h.defaultRuleName()
	}

	d, err := h.Limiter.AllowKey(r.Context(), req.Rule, req.Identifier)
	if err != nil && d.Limit == 0 {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	middleware.WriteHeaders(w, d)
	if !d.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Round(time.Second).Seconds())))
	}
	status := http.StatusOK
	if !d.Allowed {
		status = http.StatusTooManyRequests
	}
	resp := checkResponse{
		Allowed:   d.Allowed,
		Limit:     d.Limit,
		Remaining: d.Remaining,
		Rule:      req.Rule,
	}
	if !d.ResetAt.IsZero() {
		resp.ResetAt = d.ResetAt.Unix()
	}
	if !d.Allowed {
		resp.RetryAfter = int64(d.RetryAfter.Round(time.Second).Seconds())
	}
	writeJSON(w, status, resp)
}

type statusResponse struct {
	Rule      string `json:"rule"`
	Algorithm string `json:"algorithm"`
	Key       string `json:"key"`
	State     any    `json:"state"`
}

// status inspects current bucket state for a key without consuming capacity.
// The {key} path param is the identifier; the rule is selected via ?rule= or
// defaults to the first configured rule.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "key")
	ruleName := r.URL.Query().Get("rule")
	if ruleName == "" {
		ruleName = h.defaultRuleName()
	}
	rule, ok := h.Limiter.RuleByName(ruleName)
	if !ok {
		http.Error(w, "unknown rule: "+ruleName, http.StatusNotFound)
		return
	}

	key := algorithms.KeyFor(rule.Algorithm, identifier, rule.Name)
	state, err := h.inspect(r.Context(), rule, key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Rule:      rule.Name,
		Algorithm: string(rule.Algorithm),
		Key:       key,
		State:     state,
	})
}

func (h *Handler) inspect(ctx context.Context, rule middleware.CompiledRule, key string) (any, error) {
	switch rule.Algorithm {
	case config.TokenBucket:
		m, err := h.Store.HGetAll(ctx, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"capacity":   rule.Limit,
			"tokens":     m["tokens"],
			"last_refill": m["last_refill"],
		}, nil
	case config.SlidingWindow:
		count, err := h.Store.ZCard(ctx, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"limit": rule.Limit, "in_window": count}, nil
	case config.FixedWindow:
		// Active bucket key is suffixed with the window index; expose the prefix and
		// the last seen counter for that prefix using the store. Reading the exact
		// active bucket requires reproducing the bucket math, so use the namespace prefix.
		return map[string]any{"limit": rule.Limit, "note": "fixed window counter is per-window; inspect with redis-cli"}, nil
	}
	return nil, nil
}

// reset clears state for a key. Admin-only — wire auth in front for production.
func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "key")
	ruleName := r.URL.Query().Get("rule")
	if ruleName == "" {
		ruleName = h.defaultRuleName()
	}
	rule, ok := h.Limiter.RuleByName(ruleName)
	if !ok {
		http.Error(w, "unknown rule: "+ruleName, http.StatusNotFound)
		return
	}
	key := algorithms.KeyFor(rule.Algorithm, identifier, rule.Name)
	if err := h.Store.Del(r.Context(), key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Fixed window keys carry a window suffix; document the limitation rather than
	// scan-and-delete which is expensive and racy.
	if rule.Algorithm == config.FixedWindow {
		writeJSON(w, http.StatusOK, map[string]any{
			"reset":  false,
			"reason": "fixed window keys rotate per window — wait for window expiry or DEL by pattern",
			"prefix": key,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset": true, "key": key})
}

type healthResponse struct {
	Status string `json:"status"`
	Redis  string `json:"redis"`
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if err := h.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "degraded", Redis: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Redis: "ok"})
}

func (h *Handler) defaultRuleName() string {
	if len(h.Limiter.Rules) > 0 {
		return h.Limiter.Rules[0].Name
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
