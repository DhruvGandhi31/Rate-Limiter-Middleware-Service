// Package api exposes the rate-limiter as an HTTP service. Clients call
// /check to consume a token, /status to inspect state, /reset for admin
// overrides, /health for orchestration probes, and /metrics for scraping.
//
// The three helper concerns — auth, request observability, and status-class
// bucketing — live in sibling files (auth.go, observe.go) to keep this file
// focused on endpoint handlers.
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
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

// Handler bundles the moving parts every endpoint needs: the compiled
// Limiter, the raw Store for state inspection, a logger for structured
// access logging, and an optional AdminAuth for /reset.
type Handler struct {
	Limiter   *middleware.Limiter
	Store     store.Store
	Logger    *slog.Logger
	AdminAuth AdminAuth // nil = admin endpoints open (dev only — logged)
}

func NewHandler(l *middleware.Limiter, s store.Store, log *slog.Logger, auth AdminAuth) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{Limiter: l, Store: s, Logger: log, AdminAuth: auth}
}

// Routes wires the HTTP surface. observe() sits above every route so both
// /metrics and /health show up in dashboards; /reset is nested in a group
// so requireAdmin only guards the admin surface, not the public one.
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

// ---------------------------------------------------------------------------
// /check — consume a token
// ---------------------------------------------------------------------------

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
	// A non-nil err with d.Limit == 0 means the failure wasn't Redis-related
	// (the fail-mode translation would have populated Limit). Treat as 500.
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

// ---------------------------------------------------------------------------
// /status — inspect current state without consuming
// ---------------------------------------------------------------------------

type statusResponse struct {
	Rule      string `json:"rule"`
	Algorithm string `json:"algorithm"`
	Key       string `json:"key"`
	State     any    `json:"state"`
}

// status is a read-only view of the bucket for a given identifier under a
// given rule. It never consumes a token, so it's safe to poll from ops tools.
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

// inspect returns algorithm-specific state for /status. Fixed-window state
// is deliberately not surfaced: the live counter lives under a key suffixed
// with the current window index, and computing that suffix here would
// duplicate math that already lives in the algorithm's Lua script — a
// classic invitation to drift. Operators can inspect fixed-window state
// directly with `redis-cli KEYS 'ratelimit:fixed_window:<id>:*'`.
func (h *Handler) inspect(ctx context.Context, rule middleware.CompiledRule, key string) (any, error) {
	switch rule.Algorithm {
	case config.TokenBucket:
		m, err := h.Store.HGetAll(ctx, key)
		if err != nil {
			return nil, err
		}
		// Values are returned as strings on purpose — matches what redis-cli
		// HGETALL prints, which keeps ops parity with direct Redis inspection.
		return map[string]any{
			"capacity":    rule.Limit,
			"tokens":      m["tokens"],
			"last_refill": m["last_refill"],
		}, nil
	case config.SlidingWindow:
		count, err := h.Store.ZCard(ctx, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"limit": rule.Limit, "in_window": count}, nil
	case config.FixedWindow:
		return map[string]any{
			"limit": rule.Limit,
			"note":  "fixed-window counters rotate per window; inspect the live bucket with `redis-cli KEYS 'ratelimit:fixed_window:<id>:*'`",
		}, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// /reset — admin: clear state for a key
// ---------------------------------------------------------------------------

// reset drops the bucket for one identifier under one rule. Guarded by
// requireAdmin in the router. Fixed-window keys can't be fully reset in one
// call because they're suffixed by window index; the response documents
// that limitation rather than pretending a SCAN-and-DEL is safe under load.
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

// ---------------------------------------------------------------------------
// /health — liveness + Redis connectivity
// ---------------------------------------------------------------------------

type healthResponse struct {
	Status string `json:"status"`
	Redis  string `json:"redis"`
}

// health returns 200 only when Redis responds to PING within 500ms. The
// short timeout is deliberate — a health probe should fail fast when the
// dependency is degraded so a load balancer stops routing new traffic.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if err := h.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "degraded", Redis: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Redis: "ok"})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// defaultRuleName picks the first configured rule as the fallback when the
// caller doesn't specify one. Order matches config.yaml top-to-bottom.
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
