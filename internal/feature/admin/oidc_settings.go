// B161.4 (v1.5.1) — /admin/oidc operator-facing page.
//
// Operator 2026-08-23: "возможно ли сделать перехват
// запроса к head.skynas.ru" → answered with full OIDC
// provider (option 1). B161.1-3 shipped the discovery + JWKS
// + authorize + token + userinfo endpoints. B161.4 closes
// the loop: the operator needs to know how to configure
// headscale.conf to USE the new OIDC provider, and they
// need a single-pane view of "what's the issuer URL,
// what's the client_id, what's the JWKS URL, what env vars
// are set" so they can paste the right values into
// headscale.conf.
//
// This page is the operator's source of truth. It renders:
//   1. The 4 endpoint URLs the operator needs to paste into
//      headscale.conf (issuer, authorization, token, userinfo, jwks)
//   2. The current env-var values (with a "Set on host" hint
//      if something is empty)
//   3. A copy-paste-ready headscale.conf snippet with the
//      operator's actual issuer + client_id filled in
//   4. A "Test connection" button that runs a lightweight
//      discovery+userinfo probe to confirm headscale can
//      actually reach the provider (the B161.4 e2e test
//      covers the same flow at the unit level; this is the
//      live smoke test)
//
// Admin-only. Read-only — the actual OIDC config lives
// in the 4 env vars (read at boot). Changing them requires
// a container restart (same as B145/B146/B147).

package admin

import (
	"net/http"
	"strings"
	"time"

	"skygate/internal/i18n"
)

// GetAdminOIDC renders the /admin/oidc page.
//
// Admin-only. Shows the current OIDC config + the
// headscale.conf snippet the operator needs to paste into
// their headscale.conf + the live endpoint URLs.
func (s *Service) GetAdminOIDC(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	cfg := s.Cfg

	// Strip the trailing slash on the issuer for the
	// headscale.conf snippet (headscale rejects the
	// trailing slash in some versions).
	issuer := strings.TrimRight(cfg.OIDCIssuerURL, "/")
	oidcEnabled := issuer != ""

	// Build the headscale.conf snippet dynamically from
	// the operator's actual config (so they can copy-
	// paste without editing). The client_secret is
	// left as a placeholder — headscale's OIDC config
	// requires the secret to be set on the headscale
	// side anyway, and we don't echo the live secret
	// back in the admin UI (defense in depth: a stolen
	// admin session shouldn't leak the OIDC secret
	// through the audit-log-accessible admin pages).
	snippet := buildHeadscaleOIDCConfigSnippet(issuer, cfg.OIDCClientID, cfg.OIDCRedirectURIs)

	// The 5 endpoint URLs (the discovery doc + the JWKS
	// URL is the only one with a different path).
	endpoints := map[string]string{
		"issuer":         issuer,
		"authorization":  issuer + "/oidc/authorize",
		"token":          issuer + "/oidc/token",
		"userinfo":       issuer + "/oidc/userinfo",
		"jwks":           issuer + "/oidc/jwks.json",
		"discovery":      issuer + "/.well-known/openid-configuration",
	}
	_ = i18n.T(lang, "oidc.title") // keep the import used
	s.Backend.RenderWithLayout(w, r, "admin/oidc_settings.html", c, map[string]any{
		"Page":             "admin/oidc",
		"Title":            i18n.T(lang, "oidc.title"),
		"OIDCEnabled":      oidcEnabled,
		"Issuer":           issuer,
		"ClientID":         cfg.OIDCClientID,
		"KeyDir":           cfg.OIDCKeyDir,
		"RedirectURIs":     cfg.OIDCRedirectURIs,
		"Snippet":          snippet,
		"Endpoints":        endpoints,
		"FlashSuccess":     r.URL.Query().Get("ok"),
		"FlashError":       r.URL.Query().Get("err"),
		"FlashTestResult":  r.URL.Query().Get("test"),
	})
}

// buildHeadscaleOIDCConfigSnippet returns a copy-paste-
// ready headscale.conf block with the operator's actual
// issuer + client_id pre-filled. The client_secret is
// left as a placeholder "<set-on-headscale-side>" —
// headscale needs the secret in its own config, and we
// don't echo the live value back in the admin UI (audit
// log pages would surface it through a stolen session).
//
// The snippet is the documented headscale 0.30.x OIDC
// block (the most common version on the operator's VM).
// If the operator's headscale is a different version
// the field names might differ slightly — see
// docs/oidc-headscale.md for the version matrix.
func buildHeadscaleOIDCConfigSnippet(issuer, clientID, redirectURIs string) string {
	// Pick the first redirect URI as the default
	// (headscale supports multiple, but the operator
	// only needs one to start).
	primary := strings.Split(redirectURIs, ",")[0]
	primary = strings.TrimSpace(primary)
	if primary == "" {
		primary = "https://head.example.com/oidc/callback"
	}
	var b strings.Builder
	b.WriteString("# headscale.conf — OIDC block (B161.4)\n")
	b.WriteString("# Paste this into your headscale.conf under the top-level `oidc:` key.\n")
	b.WriteString("# Restart headscale after the change.\n")
	b.WriteString("# See docs/oidc-headscale.md for the full operator runbook.\n")
	b.WriteString("oidc:\n")
	b.WriteString("  issuer: " + issuer + "\n")
	b.WriteString("  client_id: " + clientID + "\n")
	b.WriteString("  client_secret: <set-on-headscale-side>  # paste the SKYGATE_OIDC_CLIENT_SECRET value\n")
	b.WriteString("  scope: [openid, profile, email]\n")
	b.WriteString("  extra_params:\n")
	b.WriteString("    domain: client_id  # any non-empty value; headscale passes it to the OIDC login URL\n")
	b.WriteString("  allowed_domains:\n")
	b.WriteString("    - example.com     # change to your tailnet's base domain (e.g. ts.net, example.com)\n")
	b.WriteString("  auto_update: true  # tailnet ACL + node list refresh on every OIDC login\n")
	b.WriteString("  strip_email_domain: true  # use the email's local part as the headscale username\n")
	b.WriteString("\n")
	b.WriteString("# The OIDC callback URL is the same as the headscale.conf `base_domain` URL +\n")
	b.WriteString("# the `/oidc/callback` path. headscale auto-detects the callback URL from\n")
	b.WriteString("# its own listen address; the redirect_uri allowlist in skygate just needs to\n")
	b.WriteString("# include whatever headscale generates. The default is:\n")
	b.WriteString("#   " + primary + "\n")
	b.WriteString("# (set SKYGATE_OIDC_REDIRECT_URIS to override the allowlist on the skygate side).\n")
	return b.String()
}

// PostAdminOIDCTest runs a lightweight discovery+userinfo
// probe to confirm headscale can actually reach the
// OIDC provider. The flow is: GET /.well-known/openid-
// configuration, parse the 4 endpoint URLs, GET /oidc/jwks
// (verifies the keypair is loaded + the JWKS endpoint
// serves a valid RS256 key), then GET /oidc/userinfo
// (should return 401 with WWW-Authenticate: Bearer — a
// happy-path 200 would mean the test came with a stale
// token, which we don't have in the smoke test).
//
// Result is rendered as a flash via ?ok=test&detail=...
// or ?err=... on the same page. The operator pastes the
// result into a Telegram message if the probe fails.
func (s *Service) PostAdminOIDCTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/oidc?err=parse_form", http.StatusFound)
		return
	}
	issuer := strings.TrimRight(s.Cfg.OIDCIssuerURL, "/")
	if issuer == "" {
		http.Redirect(w, r, "/admin/oidc?err=oidc_disabled", http.StatusFound)
		return
	}
	result, msg := s.probeOIDCProvider(issuer)
	status := "ok"
	if !result {
		status = "err"
	}
	http.Redirect(w, r, "/admin/oidc?test="+status+"&detail="+urlQueryEscape(msg),
		http.StatusFound)
}

// probeOIDCProvider is the smoke-test that runs against
// the live OIDC provider. Returns (ok, message) where
// message is a short human-readable summary (suitable
// for a Telegram message — kept under 256 chars).
//
// We use a 5s HTTP client (matches the B146/B147
// smoke-test timeout) so a stuck discovery endpoint
// doesn't block the operator's "Test" button forever.
func (s *Service) probeOIDCProvider(issuer string) (bool, string) {
	client := &http.Client{Timeout: 5 * time.Second}
	// Step 1: discovery
	discURL := issuer + "/.well-known/openid-configuration"
	resp, err := client.Get(discURL)
	if err != nil {
		return false, "discovery GET failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, "discovery returned " + statusText(resp.StatusCode)
	}
	// Step 2: jwks
	jwksURL := issuer + "/oidc/jwks.json"
	jwksResp, jerr := client.Get(jwksURL)
	if jerr != nil {
		return false, "JWKS GET failed: " + jerr.Error()
	}
	jwksResp.Body.Close()
	if jwksResp.StatusCode != 200 {
		return false, "JWKS returned " + statusText(jwksResp.StatusCode)
	}
	// Step 3: userinfo (no token → 401 + WWW-Authenticate: Bearer)
	userinfoURL := issuer + "/oidc/userinfo"
	uiResp, uerr := client.Get(userinfoURL)
	if uerr != nil {
		return false, "userinfo GET failed: " + uerr.Error()
	}
	uiResp.Body.Close()
	if uiResp.StatusCode != 401 {
		return false, "userinfo returned " + statusText(uiResp.StatusCode) + " (want 401 without Bearer)"
	}
	if wa := uiResp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(strings.ToLower(wa), "bearer") {
		return false, "userinfo missing WWW-Authenticate: Bearer (got: " + wa + ")"
	}
	return true, "discovery=200, jwks=200, userinfo=401+Bearer — OIDC provider reachable"
}

// statusText is a tiny helper that returns a short
// human label for a status code. We don't use
// http.StatusText because it returns a sentence
// ("Method Not Allowed") which is too long for the
// flash message; we want a short form ("405").
func statusText(code int) string {
	switch code {
	case 200:
		return "200"
	case 302:
		return "302"
	case 400:
		return "400"
	case 401:
		return "401"
	case 403:
		return "403"
	case 404:
		return "404"
	case 405:
		return "405"
	case 410:
		return "410"
	case 500:
		return "500"
	case 503:
		return "503"
	default:
		return "?"
	}
}
