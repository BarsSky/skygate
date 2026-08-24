package oidc

import (
	"encoding/json"
	"net/http"
)

// JWKS is the JSON Web Key Set returned by
// /oidc/jwks.json. RFC 7517 sec 5:
//
//	{"keys": [<JWK>, <JWK>, ...]}
//
// headscale fetches this URL and uses the listed
// public keys to verify the RS256 signature on
// every id_token it receives. A missing or
// mismatched key = "invalid signature" = 401 from
// headscale. The /oidc/jwks.json handler MUST stay
// reachable (not behind auth) and MUST return at
// least the current SigningKey.
//
// B161.1 ships a single key (no rotation). When
// rotation is added (B162+), old keys stay in the
// set until all id_tokens signed with them expire
// (1h default).
type JWKS struct {
	Keys []map[string]string `json:"keys"`
}

// ServeJWKS writes the JWKS document. The current
// key is exposed as a single JWK; future B-checks
// can extend this with rotated (older) keys for
// the grace period.
//
// Cache-Control: max-age=3600 — the JWKS rarely
// changes; a 1h cache keeps headscale's per-auth
// verify fast. (Rotation would invalidate this
// cache by writing a different etag.)
func (s *Service) ServeJWKS(w http.ResponseWriter, r *http.Request) {
	if !s.Keys.Ready() {
		http.Error(w, "OIDC keypair not ready (still generating)", http.StatusServiceUnavailable)
		return
	}
	ks := s.Keys.ActiveKey()
	if ks == nil {
		http.Error(w, "OIDC keypair not loaded", http.StatusServiceUnavailable)
		return
	}
	doc := JWKS{Keys: []map[string]string{ks.JWK}}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		// Status already sent; just log.
		// The OIDC spec requires the JWKS to be
		// parseable; a partial write would still
		// fail headscale's verification.
		// (B161.2 will add structured logging.)
	}
}
