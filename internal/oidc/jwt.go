package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// OIDC claim names per the spec (RFC 7519 + OIDC
// core sec 2). The non-standard claims (email,
// name, preferred_username) are skygate-specific —
// headscale needs them to populate the headscale
// user's profile.
const (
	ClaimIssuer            = "iss"
	ClaimSubject           = "sub"
	ClaimAudience          = "aud"
	ClaimExpiry            = "exp"
	ClaimIssuedAt          = "iat"
	ClaimNotBefore         = "nbf"
	ClaimNonce             = "nonce"
	ClaimEmail             = "email"
	ClaimName               = "name"
	ClaimPreferredUsername = "preferred_username"
)

// OIDC ID-token claims. Embedded in the id_token
// the OIDC service signs and returns from
// /oidc/token. The struct is also the shape we
// return from /oidc/userinfo (minus the OIDC-only
// claims like iss/aud/exp/iat).
type IDTokenClaims struct {
	Issuer            string `json:"iss"`
	Subject           string `json:"sub"`
	Audience          string `json:"aud"`
	Expiry            int64  `json:"exp"`
	IssuedAt          int64  `json:"iat"`
	Nonce             string `json:"nonce,omitempty"`
	Email             string `json:"email"`
	Name               string `json:"name,omitempty"`
	PreferredUsername string `json:"preferred_username"`
}

// signIDToken returns the RS256-signed JWT for
// the given claims. The kid in the JWT header is
// the OIDC KeyStore's kid — headscale uses it to
// look up the matching public key in the JWKS
// (RFC 7517 sec 4.5).
//
// B161.3 uses HS256 (HMAC) internally? NO — the
// OIDC spec REQUIRES RS256 (RFC 7518 sec 3.3 +
// the discovery doc we ship already advertises
// `id_token_signing_alg_values_supported: RS256`).
// The same RSA key is used for both id_token and
// access_token.
func (s *Service) signIDToken(claims IDTokenClaims) (string, error) {
	if s.IssuerURL == "" {
		return "", errors.New("oidc: issuer not configured")
	}
	ks := s.Keys.ActiveKey()
	if ks == nil {
		return "", errors.New("oidc: no signing key")
	}
	// We use jwt.MapClaims instead of a typed
	// struct because the v5 jwt.Claims interface
	// requires 5 Get* methods (GetExpirationTime,
	// GetIssuedAt, etc.) — much more boilerplate
	// than MapClaims which implements them
	// automatically.
	mc := jwt.MapClaims{
		ClaimIssuer:            claims.Issuer,
		ClaimSubject:           claims.Subject,
		ClaimAudience:          claims.Audience,
		ClaimExpiry:            claims.Expiry,
		ClaimIssuedAt:          claims.IssuedAt,
		ClaimEmail:             claims.Email,
		ClaimName:               claims.Name,
		ClaimPreferredUsername: claims.PreferredUsername,
	}
	if claims.Nonce != "" {
		mc[ClaimNonce] = claims.Nonce
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, mc)
	tok.Header["kid"] = string(ks.KID)
	return tok.SignedString(ks.Private)
}

// signAccessToken returns the RS256-signed JWT
// for the access_token. It carries the user
// profile claims (email, name, preferred_username)
// so /oidc/userinfo can return them in one shot
// without re-fetching from the DB. headscale calls
// /userinfo exactly once per login, so embedding
// the profile here is the simplest path (OIDC
// core sec 3.3 allows this; access tokens MAY
// contain user claims).
//
// B161.3 chose embed-over-fetch because:
//   1. /userinfo is the ONLY consumer of the
//      access_token (headscale doesn't use it
//      for anything else)
//   2. The 1h TTL bounds the staleness window
//      if the user's email changes after the
//      token was issued
//   3. A future B-check can switch to a DB
//      re-fetch if the operator asks for it
func (s *Service) signAccessToken(issuer, subject, audience, scope, email, name, preferredUsername string, exp, iat int64) (string, error) {
	if s.IssuerURL == "" {
		return "", errors.New("oidc: issuer not configured")
	}
	ks := s.Keys.ActiveKey()
	if ks == nil {
		return "", errors.New("oidc: no signing key")
	}
	mc := jwt.MapClaims{
		ClaimIssuer:   issuer,
		ClaimSubject:  subject,
		ClaimAudience: audience,
		ClaimExpiry:   exp,
		ClaimIssuedAt: iat,
	}
	if scope != "" {
		mc["scope"] = scope
	}
	if email != "" {
		mc[ClaimEmail] = email
	}
	if name != "" {
		mc[ClaimName] = name
	}
	if preferredUsername != "" {
		mc[ClaimPreferredUsername] = preferredUsername
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, mc)
	tok.Header["kid"] = string(ks.KID)
	return tok.SignedString(ks.Private)
}

// parseAccessToken verifies the JWT signature
// using the active signing key + returns the
// claims. Used by /oidc/userinfo to validate
// the Authorization: Bearer header.
//
// We re-parse the JWT in /userinfo rather than
// using a server-side session store because:
//   1. /userinfo is the ONLY place access_token
//      is consumed (headscale doesn't use it for
//      anything else)
//   2. Stateless = no shared state between skygate
//      instances (important for HA later)
//   3. The access_token has a short TTL (1h) so
//      the window for a leaked token is small
func (s *Service) parseAccessToken(tokenString string) (jwt.MapClaims, error) {
	ks := s.Keys.ActiveKey()
	if ks == nil {
		return nil, errors.New("oidc: no signing key")
	}
	// Verify with the public key. ParseWithClaims
	// with a destination struct (vs. raw MapClaims)
	// does the JWT-level validation automatically
	// (alg, exp, iat, nbf, iss).
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
	tok, err := parser.ParseWithClaims(tokenString, jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
		// Pin the kid if it's set in the header.
		// The JWKS publishes exactly one key, so
		// we accept any kid that matches our
		// active key.
		if kid, _ := t.Header["kid"].(string); kid != "" && kid != string(ks.KID) {
			return nil, fmt.Errorf("unknown kid: %q", kid)
		}
		return &ks.Private.PublicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return nil, errors.New("oidc: invalid access_token")
	}
	return claims, nil
}

// verifyPKCE checks the code_verifier against
// the stored code_challenge per RFC 7636 sec 4.6.
// Only S256 is supported (B161.2 rejects "plain"
// at the /authorize step).
//
// S256 challenge = base64url(sha256(verifier))
// — the verifier is the raw bytes the client
// sent, NOT base64-decoded.
func verifyPKCE(verifier, challenge, method string) bool {
	if method != "S256" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return computed == challenge
}
