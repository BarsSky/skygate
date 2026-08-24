package oidc

import (
	"log"
	"net/http"
)

// Service holds the OIDC provider state: the
// configured issuer URL + the loaded RSA keypair.
// All public methods (ServeDiscoveryDoc, ServeJWKS)
// are HTTP handler functions that take an *App
// receiver so they can be mounted directly in
// main.go as `mux.HandleFunc("GET /.well-known/
// openid-configuration", oidcSvc.ServeDiscoveryDoc)`.
//
// B161.1 lifecycle: NewService() is called from
// main.go at boot. The keypair is loaded (or
// generated) synchronously; if the key directory
// doesn't exist + RSA generation fails (disk full,
// permission denied), main.go aborts startup — a
// half-loaded OIDC provider is worse than no
// provider, since headscale would see random
// 500s and the operator would have to debug.
//
// B161.2 will add the auth code store + the
// /oidc/authorize handler. B161.3 will add the
// /oidc/token + /oidc/userinfo handlers.
type Service struct {
	// IssuerURL is the public URL of skygate that
	// headscale is configured to trust (e.g.
	// "https://skygate.example.com"). Used as the
	// "iss" claim in id_tokens + as the base for
	// all discovery-doc endpoint URLs. Empty =
	// provider disabled (all handlers return 503).
	IssuerURL string
	// ClientID + ClientSecret are the credentials
	// headscale presents in the /oidc/token request
	// (form-encoded client_id + client_secret).
	// Stored in the OIDC config (config.Config) so
	// operators can rotate them via env vars. The
	// same pair MUST be set in headscale.conf.
	ClientID     string
	ClientSecret string
	// RedirectURIs is a comma-separated allowlist
	// for the redirect_uri parameter on
	// /oidc/authorize. RFC 6749 sec 3.1.2.3
	// requires exact-string match. B161.2.
	RedirectURIs string
	// Keys is the RSA keypair for signing id_tokens
	// (B161.3) + exposing the public key in JWKS
	// (B161.1, this commit).
	Keys *KeyStore
	// Codes is the in-memory store of pending
	// auth codes. B161.2.
	Codes *AuthCodeStore
}

// NewService loads the RSA keypair (or generates
// one if missing) and returns a ready Service. If
// the key directory can't be created or the
// keypair can't be loaded, returns an error — main.go
// should abort startup (we don't want a partially-
// configured OIDC provider).
//
// B161.1 only mounts the discovery + JWKS handlers
// (in main.go). B161.2 / B161.3 will extend this
// constructor with the auth code store + token
// store.
func NewService(issuerURL, clientID, clientSecret, keyDir, redirectURIs string) (*Service, error) {
	if issuerURL == "" {
		// Provider disabled — main.go can still
		// mount the routes (they'll return 503)
		// so a future issuer URL takes effect
		// without a code change. Or main.go can
		// skip mounting entirely; both work.
		log.Printf("oidc: SKYGATE_OIDC_ISSUER not set — OIDC routes will return 503 until configured")
	} else {
		log.Printf("oidc: provider enabled issuer=%s client_id=%s redirect_uris=%d",
			issuerURL, clientID, len(redirectURIs))
	}
	keys, err := NewKeyStore(keyDir)
	if err != nil {
		return nil, err
	}
	return &Service{
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURIs: redirectURIs,
		Keys:         keys,
		Codes:        NewAuthCodeStore(),
	}, nil
}

// Handler returns a tiny http.Handler that mounts
// all OIDC endpoints. Used by main.go:
//
//	mux.Handle("/.well-known/", oidcSvc.Handler())
//	mux.Handle("/oidc/", oidcSvc.Handler())
//
// B161.3 completes the v1 OIDC surface:
// discovery + JWKS (B161.1), /authorize
// (B161.2), /token + /userinfo (this commit).
// A future B-check (B161.5+) will add the
// consent screen + refresh tokens.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.ServeDiscoveryDoc)
	mux.HandleFunc("GET /oidc/jwks.json", s.ServeJWKS)
	mux.HandleFunc("GET /oidc/authorize", s.ServeAuthorize)
	mux.HandleFunc("POST /oidc/token", s.ServeToken)
	mux.HandleFunc("GET /oidc/userinfo", s.ServeUserinfo)
	return mux
}
