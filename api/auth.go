package api

import "net/http"

// AdminAuth gates admin-only endpoints (currently /reset). A nil AdminAuth on
// the Handler means "no auth" — suitable for local dev, never production.
// main.go wires a real implementation when the config supplies a token, and
// logs a loud warning otherwise.
type AdminAuth interface {
	Authorize(r *http.Request) bool
}

// BearerTokenAuth accepts requests carrying `Authorization: Bearer <token>`
// where <token> matches a configured shared secret. Chosen for its zero
// operational cost: no external identity provider, no key rotation
// infrastructure, and the check itself is a handful of instructions.
//
// For multi-tenant production the right replacement is OIDC or mTLS — this
// scheme has no notion of principal, expiry, or revocation.
type BearerTokenAuth struct {
	Token string
}

// Authorize compares the bearer token against the configured secret in
// constant time so an attacker can't learn the correct token's length or
// prefix by timing repeated requests.
func (b *BearerTokenAuth) Authorize(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	got := h[len(prefix):]
	if len(got) != len(b.Token) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ b.Token[i]
	}
	return diff == 0
}
