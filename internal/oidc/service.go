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
	// (B161.1, this commit). B270: it is nil when the
	// key store could not be created — KeyStore.Ready()
	// is nil-safe and every signing path degrades to a
	// 503, so the process still serves the portal.
	Keys *KeyStore
	// KeyStoreErr is the reason the key store is
	// unavailable (empty when Keys is usable). Rendered
	// by the OIDC routes so an operator sees WHY instead
	// of a generic 503. B270.
	KeyStoreErr string
	// Codes is the in-memory store of pending
	// auth codes. B161.2.
	Codes *AuthCodeStore
	// JWTSecret is the HMAC secret used to verify
	// the skygate_session cookie on /oidc/authorize.
	// The OIDC handler must read the same session
	// cookie that PostLogin sets (which is an HS256
	// JWT containing uid + usr + adm claims), NOT
	// some other format. Pre-B174 the OIDC handler
	// tried to parse the cookie value as
	// "<uid>:<username>:<email>:<expires_unix>"
	// (a colon-separated format that PostLogin
	// never used) and ALWAYS returned nil — which
	// made /oidc/authorize think the user was
	// unauthenticated even right after a successful
	// login, causing a redirect loop:
	//   /oidc/authorize → /login?next=...
	//   → POST /login → /oidc/authorize?...
	//   → /oidc/authorize → /login?next=...
	// The user saw the login page re-render with
	// an empty password ("сбрасывает"). B174 wires
	// the OIDC service to the same auth.ParseJWT
	// helper that feature/auth uses, so the session
	// is recognized and the loop is broken.
	JWTSecret string
	// UserLookup maps a JWT-claim UserID to the
	// user's current username + email. The JWT
	// cookie only carries uid + usr (no email),
	// so the OIDC handler needs a DB-side lookup
	// to populate the id_token /userinfo email
	// claim. Returns (username, email, error); on
	// error the user is treated as unauthenticated.
	// Wired in main.go via db.GetUserNameAndEmailByID
	// (B174 introduces that helper). Optional —
	// if nil, the email claim is left empty.
	UserLookup func(userID int64) (username, email string, err error)
}

// NewService loads the RSA keypair (or generates
// one if missing) and returns a ready Service.
//
// B270 (2026-09-19): a key-store failure is NO LONGER
// fatal to the process. Live case: a native install
// whose working directory was not writable ran with
// SKYGATE_OIDC_KEY_DIR at its relative default
// (./data/oidc-keys); NewKeyStore's MkdirAll failed
// with "mkdir ./data: permission denied", main.go
// called log.Fatalf — and skygate died BEFORE it
// bound its HTTP port. The unit was 'active' with
// nothing listening, the self-updater could never
// verify the new build, and the reason was a side
// feature nobody was even using (SKYGATE_OIDC_ISSUER
// was unset). A broken OIDC provider must not take
// the control plane down with it: the key store is
// left nil, the failure is logged loudly, and every
// OIDC route answers 503 with the reason. OIDC is
// optional — /healthz, /login, the API and the UI are
// not.
func NewService(issuerURL, clientID, clientSecret, keyDir, redirectURIs, jwtSecret string) (*Service, error) {
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
		// Degrade, do not die. The Error return stays in
		// the signature for callers that want to surface
		// it, but the Service is still usable: Keys == nil
		// makes Ready() false and every signing path return
		// "oidc: no signing key" instead of panicking.
		log.Printf("oidc: KEY STORE UNAVAILABLE (%v) — the process keeps running and the OIDC routes answer 503; fix SKYGATE_OIDC_KEY_DIR (needs a directory writable by the service user, e.g. /var/lib/skygate/oidc-keys) and restart", err)
		return &Service{
			IssuerURL:    issuerURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURIs: redirectURIs,
			Codes:        NewAuthCodeStore(),
			JWTSecret:    jwtSecret,
			KeyStoreErr:  err.Error(),
		}, nil
	}
	return &Service{
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURIs: redirectURIs,
		Keys:         keys,
		Codes:        NewAuthCodeStore(),
		JWTSecret:    jwtSecret,
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
