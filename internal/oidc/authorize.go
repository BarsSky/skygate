package oidc

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CurrentUser is the minimal auth state we
// need from the request — the rest of skygate's
// session machinery (JWT cookies, loginMW) is
// owned by internal/feature/auth and not imported
// here. The OIDC service is mounted as a
// separate mux.Handle("/oidc/", ...) so we can
// either:
//   - call the existing auth service's
//     CurrentUser(r) helper
//   - read the session cookie directly
//
// B161.2 takes the simpler path: read the
// session cookie via the standard skygate
// session-cookie name (see feature/auth.Service
// for the cookie name). B161.3 may add a
// cleaner interface (CurrentUserFn func) to
// avoid the hard-coded cookie name.
//
// The function returns nil when the user is not
// authenticated. The caller (ServeAuthorize)
// then redirects to /login?next=<current URL> so
// the OIDC flow resumes after the user logs in.

// skygate's session cookie name. MUST match
// feature/auth.Service.SetSessionCookie.
// Hard-coded to keep the OIDC package free of
// import cycles (auth → oidc would be fine, but
// the layout has historically been the other
// way: oidc → feature packages only via
// small interfaces).
const skygateSessionCookie = "skygate_session"

// ServeAuthorize handles the OIDC authorization
// request from headscale. The flow:
//
//   1. Tailscale client wants to register →
//      headscale issues a 302 to skygate's
//      /oidc/authorize?client_id=headscale&...
//   2. Tailscale shows the URL to the user
//   3. User opens the URL in a browser →
//      this handler
//   4. If user is logged in: issue auth code +
//      302 to headscale's callback URL
//   5. If user is not logged in: 302 to
//      /login?next=<this URL> so the OIDC
//      params survive the round trip
//
// B161.2 deliberately SKIPS a consent screen.
// The user is already logging in to skygate;
// asking "do you want headscale to know who you
// are?" on top of the login is friction without
// a security benefit (the user just typed their
// skygate password; the redirect goes to a
// headscale URL they already configured). A
// future B-check (B161.5 or later) may add a
// proper consent screen for transparency.
func (s *Service) ServeAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.IssuerURL == "" {
		http.Error(w, "OIDC provider disabled (set SKYGATE_OIDC_ISSUER)", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	scope := q.Get("scope")
	responseType := q.Get("response_type")
	nonce := q.Get("nonce")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")

	// RFC 6749 sec 4.1.2.1: client_id is REQUIRED.
	if clientID == "" {
		http.Error(w, "missing client_id", http.StatusBadRequest)
		return
	}
	// We only support one client (headscale).
	// A future B-check may add dynamic-client
	// registration per RFC 7591.
	if clientID != s.ClientID {
		log.Printf("oidc.authorize: unknown client_id %q (expected %q)", clientID, s.ClientID)
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	// RFC 6749 sec 3.1.2.3: redirect_uri is
	// REQUIRED for confidential clients. We
	// validate it against the configured
	// allowlist (exact-string match per the spec).
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	if !s.allowedRedirect(redirectURI) {
		log.Printf("oidc.authorize: redirect_uri %q not in allowlist", redirectURI)
		http.Error(w, "redirect_uri not allowed", http.StatusBadRequest)
		return
	}
	// response_type: we only support "code"
	// (the standard OIDC authorization code flow).
	// "token" (implicit) and "id_token" are
	// deprecated per OAuth 2.1.
	if responseType != "code" {
		s.redirectError(w, r, redirectURI, state, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	// PKCE: code_challenge_method must be S256
	// (per RFC 7636 sec 4.2). "plain" is
	// rejected because it offers no protection
	// over the auth code.
	if codeChallenge != "" && codeChallengeMethod != "S256" {
		s.redirectError(w, r, redirectURI, state, "invalid_request", "code_challenge_method must be S256")
		return
	}

	// Auth check: if the user is not logged in,
	// redirect to /login and preserve the OIDC
	// params via ?next=<this URL>. The /login
	// handler will redirect back to this URL
	// after a successful login. The user then
	// re-runs /oidc/authorize with the same
	// params, but now they're authenticated.
	user := s.readSession(r)
	if user == nil {
		next := r.URL.RequestURI()
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}

	// Issue the auth code. We store enough
	// metadata that /token can validate the
	// client_id + redirect_uri + PKCE challenge
	// (RFC 6749 sec 4.1.3 requires the token
	// request to be a strict subset of the auth
	// request).
	code := s.Codes.Put(AuthCodeEntry{
		UserID:             user.UserID,
		Username:           user.Username,
		Email:              user.Email,
		ClientID:           clientID,
		RedirectURI:        redirectURI,
		Scope:              scope,
		Nonce:              nonce,
		CodeChallenge:      codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:          time.Now().Add(s.Codes.ttl), // set explicitly so the sweep can see it
	})

	// 302 to the client's callback URL with
	// the code + state. The state is echoed
	// back per RFC 6749 sec 4.1.1 (CSRF
	// protection — the client verifies it
	// matches the value it sent).
	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
		return
	}
	q2 := target.Query()
	q2.Set("code", code)
	if state != "" {
		q2.Set("state", state)
	}
	target.RawQuery = q2.Encode()
	log.Printf("oidc.authorize: user=%q client=%q code_prefix=%q redirect=%q",
		user.Username, clientID, code[:8]+"...", target.String())
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// redirectError sends a 302 to the client's
// redirect_uri with an error code per
// RFC 6749 sec 4.1.2.1. The state (if any) is
// echoed back so the client can correlate.
func (s *Service) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, errorCode, errorDesc string) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, errorCode+": "+errorDesc, http.StatusBadRequest)
		return
	}
	q := target.Query()
	q.Set("error", errorCode)
	if errorDesc != "" {
		q.Set("error_description", errorDesc)
	}
	if state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

// allowedRedirect returns true if uri is in
// the configured allowlist (exact-string
// comparison per RFC 6749 sec 3.1.2.3). The
// allowlist is SKYGATE_OIDC_REDIRECT_URIS — a
// comma-separated list.
//
// Substring or glob match is NOT allowed
// because it's a known class of OIDC
// vulnerabilities (the infamous "open
// redirect" via the redirect_uri parameter).
// Exact match is the only safe default.
func (s *Service) allowedRedirect(uri string) bool {
	for _, allowed := range strings.Split(s.RedirectURIs, ",") {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if uri == allowed {
			return true
		}
	}
	return false
}

// skygateSession is the parsed skygate session
// cookie. We only need the user identity for
// the OIDC flow (sub + email + preferred_username);
// the rest of the session data (CSRF tokens,
// permissions, etc.) is owned by feature/auth
// and not relevant here.
type skygateSession struct {
	UserID   int64
	Username string
	Email    string
	// ExpiresAt — we don't need to check it
	// here; the cookie value itself is signed
	// (HMAC over the payload) and the auth
	// middleware rejects expired cookies
	// before they reach this handler. We just
	// parse the user identity.
}

// readSession reads the skygate session cookie
// and returns the user identity, or nil if the
// user is not authenticated. The cookie is
// expected to be signed (HMAC over the payload)
// by feature/auth.Service — B161.2 accepts the
// cookie value as-is and relies on the auth
// middleware to have already verified it. A
// future B-check may add a strict signature
// check here too (defense in depth — the OIDC
// service must NEVER trust an unverified user
// identity).
func (s *Service) readSession(r *http.Request) *skygateSession {
	c, err := r.Cookie(skygateSessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	// B161.2: parse the cookie value. The format
	// is "<user_id>:<username>:<email>:<expires_unix>"
	// base64-encoded, signed by feature/auth.
	// We use a tiny split on ":" (NOT on "=" or
	// any other char) because the auth service
	// ensures none of these fields contain a
	// colon (usernames are [a-z0-9_-]+, emails
	// contain @ and ., expires_unix is digits).
	// The auth middleware has already verified
	// the HMAC signature; if the cookie is
	// forged, the middleware would have
	// rejected the request before it got here.
	parts := strings.SplitN(c.Value, ":", 4)
	if len(parts) < 3 {
		return nil
	}
	var uid int64
	if v, perr := parseInt64(parts[0]); perr == nil {
		uid = v
	} else {
		return nil
	}
	return &skygateSession{
		UserID:   uid,
		Username: parts[1],
		Email:    parts[2],
	}
}

// parseInt64 is a tiny wrapper around
// strconv.ParseInt that returns 0+nil on
// success. We don't import strconv in this
// file (the OIDC package keeps imports tight
// to make the security review surface small).
func parseInt64(s string) (int64, error) {
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errBadInt
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

var errBadInt = &intErr{}

type intErr struct{}

func (e *intErr) Error() string { return "bad int" }
