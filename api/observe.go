package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dhruvgandhi/rate-limiter/internal/metrics"
)

// observe records the HTTP-layer Prometheus metrics and one structured
// access-log line per request. It sits above every route so /metrics scrapes
// and /health probes are counted too — dashboards can then filter by route.
//
// The response writer is wrapped so we can learn the final status code
// after the inner handler has already called WriteHeader.
func (h *Handler) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		// chi resolves the route pattern lazily; fall back to the raw path when
		// the request didn't match any route so metrics still capture 404s.
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = r.URL.Path
		}
		dur := time.Since(start)
		metrics.HTTPRequestDuration.WithLabelValues(route).Observe(dur.Seconds())
		metrics.HTTPRequestsTotal.WithLabelValues(route, statusClass(ww.status)).Inc()

		// Prometheus scrapes /metrics on a tight interval; logging every scrape at
		// info level would drown out real request traffic.
		if route == "/metrics" {
			h.Logger.Debug("http", "method", r.Method, "route", route, "status", ww.status, "duration_ms", dur.Milliseconds())
			return
		}
		h.Logger.Info("http", "method", r.Method, "route", route, "status", ww.status, "duration_ms", dur.Milliseconds())
	})
}

// requireAdmin gates admin-only routes. With no AdminAuth configured (dev
// mode) it lets the request through but logs a warning per hit so an
// unlocked deployment shows up loudly in the access log — not silently.
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

// statusRecorder intercepts WriteHeader so observe() can label metrics by
// status class. Default is 200 because handlers that only call Write() (no
// explicit WriteHeader) still produce a 200 response.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// statusClass buckets HTTP status codes into the standard 2xx/3xx/4xx/5xx
// families so Prometheus queries can rate-of-errors without knowing every code.
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
