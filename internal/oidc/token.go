package oidc

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// TokenResponse is the JSON body of a successful
// /oidc/token response (RFC 6749 sec 5.1).
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	IDToken     string `json:"id_token,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

// TokenErrorResponse is the JSON body of a failed
// /oidc/token response (RFC 6749 sec 5.2). The
// error description is optional and shouldn't
// include sensitive details (per the spec).
type TokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// Token TTLs (in seconds). B161.3 default matches
// the OIDC core spec example (1h for both). The
// access_token has a shorter "validity window"
// than the id_token, but for the v1 OIDC flow
// (no refresh tokens, no long-running sessions)
// 1h is the right default.
//
// A future B-check (B161.5+) may add refresh
// token support for the "remember me" UX (the
// user can stay logged in for weeks without
// re-entering their password).
const (
	idTokenTTLSeconds     = 3600 // 1h
	accessTokenTTLSeconds = 3600 // 1h
)

// ServeToken handles the OIDC token request.
// Flow (RFC 6749 sec 4.1.3):
//
//  1. headscale POSTs to /oidc/token with:
//     - grant_type=authorization_code
//     - code=<the code from /authorize>
//     - client_id, client_secret (auth)
//     - redirect_uri (must match the one in /authorize)
//     - code_verifier (PKCE, if used)
//  2. We validate all of the above
//  3. We look up the auth code in the store
//     (consuming it — single use)
//  4. We sign the id_token + access_token (RS256)
//  5. We return the JSON token response
//
// On any error we return a 400 with a token-error
// JSON body (NOT a 302 to a callback URL — the
// token endpoint is server-to-server, not
// browser-to-server).
func (s *Service) ServeToken(w http.ResponseWriter, r *http.Request) {
	if s.IssuerURL == "" {
		s.tokenError(w, "server_error", "OIDC provider disabled")
		return
	}
	if r.Method != http.MethodPost {
		s.tokenError(w, "invalid_request", "POST required")
		return
	}
	// RFC 6749 sec 4.1.3: client can authenticate
	// via either:
	//   1. client_secret_post: form parameters
	//      client_id + client_secret (what we
	//      advertise in the discovery doc as
	//      `token_endpoint_auth_methods_supported`)
	//   2. client_secret_basic: HTTP Basic auth
	//      header
	// B161.3 supports BOTH (the discovery doc
	// lists only `client_secret_post` for v1; a
	// future B-check can add `client_secret_basic`
	// to the discovery doc when both are wired).
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	if clientID == "" || clientSecret == "" {
		if u, p, ok := r.BasicAuth(); ok {
			clientID, clientSecret = u, p
		}
	}
	if clientID == "" {
		s.tokenError(w, "invalid_client", "missing client_id")
		return
	}
	if clientID != s.ClientID {
		log.Printf("oidc.token: unknown client_id %q", clientID)
		s.tokenError(w, "invalid_client", "unknown client_id")
		return
	}
	// Constant-time comparison to prevent
	// timing attacks on the secret. (Even though
	// headscale's network is the only client,
	// defense in depth.)
	if !secureEqual(clientSecret, s.ClientSecret) {
		log.Printf("oidc.token: bad client_secret for %q", clientID)
		s.tokenError(w, "invalid_client", "bad client_secret")
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" {
		s.tokenError(w, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	code := r.FormValue("code")
	if code == "" {
		s.tokenError(w, "invalid_request", "missing code")
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	if redirectURI == "" {
		s.tokenError(w, "invalid_request", "missing redirect_uri")
		return
	}
	codeVerifier := r.FormValue("code_verifier")

	// Look up + consume the code. Get() returns
	// false if the code is unknown or expired —
	// RFC 6749 sec 4.1.2 requires single-use, so
	// Get() deletes the entry on read.
	entry, ok := s.Codes.Get(code)
	if !ok {
		log.Printf("oidc.token: unknown or expired code prefix=%q", code[:min(8, len(code))])
		s.tokenError(w, "invalid_grant", "code unknown or expired")
		return
	}
	// Re-validate client_id + redirect_uri. The
	// client MUST send the same values it sent
	// in /authorize. This is the
	// token-side-of-redirect_uri defense.
	if entry.ClientID != clientID {
		log.Printf("oidc.token: client_id mismatch code=%q got=%q", clientID, entry.ClientID)
		s.tokenError(w, "invalid_grant", "client_id mismatch")
		return
	}
	if entry.RedirectURI != redirectURI {
		log.Printf("oidc.token: redirect_uri mismatch")
		s.tokenError(w, "invalid_grant", "redirect_uri mismatch")
		return
	}
	// PKCE (RFC 7636 sec 4.6): if the /authorize
	// request included a code_challenge, the
	// /token request MUST include a matching
	// code_verifier. We recompute S256(verifier)
	// and compare to the stored challenge.
	if entry.CodeChallenge != "" {
		if codeVerifier == "" {
			s.tokenError(w, "invalid_grant", "missing code_verifier (PKCE required)")
			return
		}
		if !verifyPKCE(codeVerifier, entry.CodeChallenge, entry.CodeChallengeMethod) {
			log.Printf("oidc.token: PKCE verification failed")
			s.tokenError(w, "invalid_grant", "code_verifier does not match")
			return
		}
	}

	// Issue the tokens.
	now := time.Now().Unix()
	exp := now + idTokenTTLSeconds
	idTok, err := s.signIDToken(IDTokenClaims{
		Issuer:    s.IssuerURL,
		Subject:   entry.Username, // RFC 7519 sec 4.1.2: sub is the user identifier
		Audience:  clientID,
		Expiry:    exp,
		IssuedAt:  now,
		Nonce:     entry.Nonce,
		Email:     entry.Email,
		Name:      entry.Username, // skygate doesn't have a separate "display name" column; use username
		PreferredUsername: entry.Username,
	})
	if err != nil {
		log.Printf("oidc.token: signIDToken: %v", err)
		s.tokenError(w, "server_error", "id_token sign failed")
		return
	}
	accessTok, err := s.signAccessToken(
		s.IssuerURL,
		entry.Username,
		clientID,
		entry.Scope,
		entry.Email,
		entry.Username, // skygate doesn't have a separate "display name" column
		entry.Username,
		now+accessTokenTTLSeconds,
		now,
	)
	if err != nil {
		log.Printf("oidc.token: signAccessToken: %v", err)
		s.tokenError(w, "server_error", "access_token sign failed")
		return
	}

	resp := TokenResponse{
		AccessToken: accessTok,
		TokenType:   "Bearer",
		ExpiresIn:   accessTokenTTLSeconds,
		IDToken:     idTok,
		Scope:       entry.Scope,
	}
	log.Printf("oidc.token: issued tokens user=%q client=%q scope=%q",
		entry.Username, clientID, entry.Scope)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// tokenError writes a 400 + JSON body with the
// OIDC token-error shape (RFC 6749 sec 5.2).
// The 400 is standard for "the request was
// malformed or the credentials are invalid";
// 401 is reserved for "you need to authenticate"
// (which is the client_secret case, but RFC
// 6749 sec 5.2 says use 400 with invalid_client
// in the body).
func (s *Service) tokenError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(TokenErrorResponse{
		Error:            code,
		ErrorDescription: desc,
	})
}

// secureEqual is constant-time string compare
// to prevent timing attacks on the
// client_secret. The OIDC spec doesn't strictly
// require this for client_secret (vs. user
// password), but defense in depth is cheap.
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
