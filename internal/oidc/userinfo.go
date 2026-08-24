package oidc

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// UserinfoResponse is the JSON body of a
// successful /oidc/userinfo response (OIDC
// core sec 5.3). The set of claims returned
// depends on the scopes the user authorized:
//   - openid: sub (always)
//   - profile: name, preferred_username
//   - email: email
//
// B161.3 returns all the claims we have. A
// future B-check may filter based on the
// scopes in the access_token (we have them
// in the JWT).
type UserinfoResponse struct {
	Sub               string `json:"sub"`
	Email             string `json:"email,omitempty"`
	Name               string `json:"name,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
}

// ServeUserinfo handles the OIDC userinfo
// request. Flow (OIDC core sec 5.3):
//
//  1. headscale GETs /oidc/userinfo with
//     Authorization: Bearer <access_token>
//  2. We verify the access_token (RS256 sig +
//     not-expired)
//  3. We look up the user from the JWT's
//     `sub` claim (== skygate username; the
//     id_token + access_token have the same
//     `sub`)
//  4. We return the user claims
//
// We could call db.GetUserByUsername here to
// re-fetch the email (in case it changed since
// the access_token was issued), but the access
// token TTL is 1h so the staleness window is
// bounded. The id_token is the source of truth
// for the auth — we re-issue on every /authorize
// + /token round trip.
//
// B161.3 uses the JWT's `sub` claim as the
// canonical user identifier. The JWT itself
// was signed by us in /oidc/token, so the
// `sub` is trustworthy (a malicious client
// can't forge it — they'd need our private key).
func (s *Service) ServeUserinfo(w http.ResponseWriter, r *http.Request) {
	if s.IssuerURL == "" {
		http.Error(w, "OIDC provider disabled", http.StatusServiceUnavailable)
		return
	}
	// Only GET is allowed (OIDC core sec 5.3).
	// POST is allowed per the spec but headscale
	// uses GET; we lock down for clarity.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Extract the access_token from the
	// Authorization header. Per RFC 6750 sec 2.1
	// the scheme MUST be "Bearer".
	auth := r.Header.Get("Authorization")
	if auth == "" {
		s.userinfoError(w, "invalid_token", "missing Authorization header", http.StatusUnauthorized)
		return
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		s.userinfoError(w, "invalid_token", "Authorization must be 'Bearer <token>'", http.StatusUnauthorized)
		return
	}
	tokenString := parts[1]
	claims, err := s.parseAccessToken(tokenString)
	if err != nil {
		log.Printf("oidc.userinfo: parseAccessToken: %v", err)
		s.userinfoError(w, "invalid_token", "access_token invalid or expired", http.StatusUnauthorized)
		return
	}
	// Extract the sub (the skygate username we
	// used when issuing the token). B161.3
	// doesn't re-fetch from the DB — the
	// access_token JWT is the source of truth
	// (signed by us, not forgeable by the
	// client). The id_token has the same `sub`
	// (set in /oidc/token).
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)
	pref, _ := claims["preferred_username"].(string)
	if sub == "" {
		s.userinfoError(w, "invalid_token", "missing sub claim", http.StatusUnauthorized)
		return
	}
	resp := UserinfoResponse{
		Sub:               sub,
		Email:             email,
		Name:               name,
		PreferredUsername: pref,
	}
	// OIDC core sec 5.3: when the access_token
	// is valid, return 200 + JSON. The Content-Type
	// is application/json (no charset) per the
	// spec. We add charset=utf-8 for browser
	// compatibility (no functional impact).
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// userinfoError writes a 401 (or the provided
// status) + JSON body with the OAuth error
// shape (RFC 6750 sec 3 + OIDC core sec 5.3).
// The 401 is the standard for "your token is
// invalid or expired" — headscale will retry
// the OIDC flow if /userinfo returns 401.
func (s *Service) userinfoError(w http.ResponseWriter, errorCode, desc string, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// RFC 6750 sec 3: a 401 response MUST include
	// a WWW-Authenticate header with the error.
	w.Header().Set("WWW-Authenticate",
		`Bearer error="`+errorCode+`", error_description="`+desc+`"`)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errorCode,
		"error_description": desc,
	})
}
