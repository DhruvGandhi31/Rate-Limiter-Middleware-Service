package unit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhruvgandhi/rate-limiter/api"
	"github.com/dhruvgandhi/rate-limiter/internal/config"
	"github.com/dhruvgandhi/rate-limiter/internal/middleware"
	"github.com/dhruvgandhi/rate-limiter/internal/store"
)

func newTestHandler(t *testing.T, rule config.Rule) (http.Handler, *store.Fake) {
	t.Helper()
	return newTestHandlerWithAuth(t, rule, nil)
}

func newTestHandlerWithAuth(t *testing.T, rule config.Rule, auth api.AdminAuth) (http.Handler, *store.Fake) {
	t.Helper()
	cfg := &config.Config{FailMode: config.FailClosed, Rules: []config.Rule{rule}}
	s := store.NewFake()
	l, err := middleware.New(cfg, s)
	require.NoError(t, err)
	return api.NewHandler(l, s, nil, auth).Routes(), s
}

func TestCheckEndpoint_AllowsThenRejects(t *testing.T) {
	h, _ := newTestHandler(t, config.Rule{
		Name: "test", Identifier: "api_key", Algorithm: config.FixedWindow,
		Limit: 2, Window: 5 * time.Second,
	})

	body := func() *bytes.Buffer {
		b, _ := json.Marshal(map[string]string{"rule": "test", "identifier": "user-1"})
		return bytes.NewBuffer(b)
	}

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/check", body()))
		assert.Equal(t, http.StatusOK, w.Code, "request %d", i)
		assert.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/check", body()))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

func TestHealthEndpoint(t *testing.T) {
	h, _ := newTestHandler(t, config.Rule{
		Name: "default", Algorithm: config.TokenBucket, Limit: 1, RefillRate: 1,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAdminAuth_RequiredForReset(t *testing.T) {
	h, _ := newTestHandlerWithAuth(t, config.Rule{
		Name: "tb", Identifier: "api_key", Algorithm: config.TokenBucket,
		Limit: 1, RefillRate: 0.0001,
	}, &api.BearerTokenAuth{Token: "secret-token"})

	// No header → 401
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/reset/u?rule=tb", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Wrong token → 401
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/reset/u?rule=tb", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Right token → 200
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/reset/u?rule=tb", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMetricsEndpoint_Exposed(t *testing.T) {
	h, _ := newTestHandler(t, config.Rule{
		Name: "m", Algorithm: config.TokenBucket, Limit: 5, RefillRate: 5,
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ratelimit_requests_total")
}

func TestResetEndpoint_TokenBucket(t *testing.T) {
	h, _ := newTestHandler(t, config.Rule{
		Name: "tb", Identifier: "api_key", Algorithm: config.TokenBucket,
		Limit: 1, RefillRate: 0.0001,
	})

	body := func() *bytes.Buffer {
		b, _ := json.Marshal(map[string]string{"rule": "tb", "identifier": "u"})
		return bytes.NewBuffer(b)
	}

	// consume the only token
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/check", body()))
	require.Equal(t, http.StatusOK, w.Code)

	// next should fail
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/check", body()))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	// reset, then check again
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/reset/u?rule=tb", nil))
	require.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/check", body()))
	assert.Equal(t, http.StatusOK, w.Code, "after reset, should be allowed again")
}
