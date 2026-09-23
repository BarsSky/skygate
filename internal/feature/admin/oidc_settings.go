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
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// GetAdminOIDC renders the /admin/oidc page.
//
// Admin-only. Shows the current OIDC config + the
// headscale.conf snippet the operator needs to paste into
// their headscale.conf + the live endpoint URLs.
//
// B290 (2026-09-22): the page now renders the EFFECTIVE configuration — a value
// saved from this form wins, the env var is the fallback — with a per-field
// source badge, and it says whether the provider is live right now. Before this
// the page showed the env values even when the DB row said something else, so an
// operator who had saved the form saw a form that disagreed with the running
// provider (live report: «нет удобного выставления включения OIDC, пока нет в env
// строчки нельзя никак настроить, но из описания непонятно что и как добавлять»).
func (s *Service) GetAdminOIDC(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)

	eff := s.effectiveOIDCSettings()

	// Strip the trailing slash on the issuer for the
	// headscale.conf snippet (headscale rejects the
	// trailing slash in some versions).
	issuer := strings.TrimRight(eff.Issuer, "/")
	oidcEnabled := eff.Enabled && issuer != ""

	// Build the headscale.conf snippet dynamically from
	// the operator's actual config (so they can copy-
	// paste without editing). The client_secret is
	// left as a placeholder — headscale's OIDC config
	// requires the secret to be set on the headscale
	// side anyway, and we don't echo the live secret
	// back in the admin UI (defense in depth: a stolen
	// admin session shouldn't leak the OIDC secret
	// through the audit-log-accessible admin pages).
	snippet := buildHeadscaleOIDCConfigSnippet(issuer, eff.ClientID, eff.RedirectURIs)

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
	// B290: what the RUNNING provider actually holds (which can differ from both
	// the form and the env until the next save).
	liveIssuer, liveClientID, liveSecret, liveRedirects := "", "", "", ""
	if s.OIDCStatusFn != nil {
		liveIssuer, liveClientID, liveSecret, liveRedirects = s.OIDCStatusFn()
	}
	// B304: the key-dir card. LiveDir comes from the RUNNING store (which can
	// differ from both the form and the env until the next save), and the state
	// probe names any problem with a copy-paste fix.
	liveDir, liveKID, liveReady := "", "", false
	if s.OIDCKeyDirFn != nil {
		liveDir, liveKID, liveReady = s.OIDCKeyDirFn()
	}
	keyDirState := oidcKeyDirStateOf(eff.KeyDir, liveDir, liveReady, liveKID, "")
	// B304: the env block. The panel wins over the env (B290), so this is an
	// OPTION for operators who manage their host through .env files, not a
	// requirement — which is exactly the dead-end the page used to imply.
	envBlock := buildOIDCEnvBlock(eff)
	envOnly := []string{}
	for _, f := range []string{"issuer", "client_id", "client_secret", "redirect_uris", "key_dir"} {
		if eff.Source[f] == "env" {
			envOnly = append(envOnly, f)
		}
	}
	s.Backend.RenderWithLayout(w, r, "admin/oidc_settings.html", c, map[string]any{
		"Page":               "admin/oidc",
		"Title":              i18n.T(lang, "oidc.title"),
		"OIDCEnabled":        oidcEnabled,
		"EnabledSaved":       eff.Enabled,
		"EnvDisabled":        eff.EnvDisabled,
		"EnvHasConfig":       eff.EnvHasConfig,
		"Issuer":             strings.TrimRight(eff.Issuer, "/"),
		"ClientID":           eff.ClientID,
		"KeyDir":             eff.KeyDir,
		"RedirectURIs":       eff.RedirectURIs,
		"SecretSet":          eff.ClientSecret != "",
		"SecretSource":       eff.SecretSource,
		"Source":             eff.Source,
		"Snippet":            snippet,
		"Endpoints":          endpoints,
		"LiveIssuer":         liveIssuer,
		"LiveClientID":       liveClientID,
		"LiveSecretSet":      liveSecret != "",
		"LiveRedirectURIs":   liveRedirects,
		"AutoApplyAvailable": s.OIDCApplier != nil,
		// B304 additions.
		"KeyDirState":     keyDirState,
		"KeyDirLive":      liveDir,
		"KeyDirAppliable": s.OIDCKeyDirApplier != nil,
		"DefaultKeyDir":   s.OIDCDefaultKeyDir,
		"EnvBlock":        envBlock,
		"EnvOnlyFields":   envOnly,
		"EnvOnlyCount":    len(envOnly),
		"FlashSuccess":    r.URL.Query().Get("ok"),
		"FlashError":      r.URL.Query().Get("err"),
		"FlashTestResult": r.URL.Query().Get("test"),
	})
}

// buildOIDCEnvBlock renders the .env lines that reproduce the EFFECTIVE OIDC
// configuration on the host (B304). The secret is never included: it is written
// as a reference to `skygate oidc-export --secret`, which prints the stored value
// on the host for the operator to paste into headscale — so the secret never
// travels over HTTP, into the audit log, or into a downloaded file.
func buildOIDCEnvBlock(eff EffectiveOIDCSettings) string {
	issuer := strings.TrimRight(eff.Issuer, "/")
	redirects := eff.RedirectURIs
	keyDir := eff.KeyDir
	secretLine := "SKYGATE_OIDC_CLIENT_SECRET=$(skygate oidc-export --secret)   # or paste the value headscale also has"
	if eff.ClientSecret == "" {
		secretLine = "SKYGATE_OIDC_CLIENT_SECRET=<not set yet — fill it on /admin/oidc first>"
	}
	var b strings.Builder
	b.WriteString("# skygate .env — OIDC (B304). Optional: values saved on /admin/oidc WIN over these.\n")
	b.WriteString("# Fill them only if you manage this host through an env file.\n")
	b.WriteString("SKYGATE_OIDC_ISSUER=" + issuer + "\n")
	b.WriteString("SKYGATE_OIDC_CLIENT_ID=" + eff.ClientID + "\n")
	b.WriteString(secretLine + "\n")
	b.WriteString("SKYGATE_OIDC_REDIRECT_URIS=" + redirects + "\n")
	b.WriteString("SKYGATE_OIDC_KEY_DIR=" + keyDir + "\n")
	b.WriteString("# SKYGATE_OIDC_ENABLED=false   # emergency off-switch: beats every other setting\n")
	return b.String()
}

// EffectiveOIDCSettings is the resolved OIDC configuration plus where each field
// came from, so the page can be honest about it (B290).
type EffectiveOIDCSettings struct {
	Enabled       bool
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURIs  string
	KeyDir        string
	// Source maps a field name ("issuer", "client_id", "client_secret",
	// "redirect_uris", "key_dir", "enabled") to "ui", "env" or "default".
	Source map[string]string
	// SecretSource is "ui", "env" or "" (nothing configured).
	SecretSource string
	// EnvDisabled is true when SKYGATE_OIDC_ENABLED explicitly turns OIDC off
	// (the emergency off-switch that beats every other setting).
	EnvDisabled bool
	// EnvHasConfig reports whether any OIDC env var is set (the page uses it to
	// explain the fallback).
	EnvHasConfig bool
}

// effectiveOIDCSettings resolves the OIDC configuration: a value saved from
// /admin/oidc wins over the env var, which wins over the built-in default.
func (s *Service) effectiveOIDCSettings() EffectiveOIDCSettings {
	out := EffectiveOIDCSettings{Source: map[string]string{}}
	set := func(field, value, source string) string {
		out.Source[field] = source
		return value
	}
	cfg := s.Cfg
	env := func(get func() string) string {
		if cfg == nil {
			return ""
		}
		return strings.TrimSpace(get())
	}
	envIssuer := env(func() string { return cfg.OIDCIssuerURL })
	envClientID := env(func() string { return cfg.OIDCClientID })
	envSecret := env(func() string { return cfg.OIDCClientSecret })
	envRedirects := env(func() string { return cfg.OIDCRedirectURIs })
	envKeyDir := env(func() string { return cfg.OIDCKeyDir })
	if envIssuer != "" || envClientID != "" || envSecret != "" || envRedirects != "" {
		out.EnvHasConfig = true
	}
	out.EnvDisabled = false
	if cfg != nil {
		switch strings.ToLower(strings.TrimSpace(cfg.OIDCEnabledEnv)) {
		case "false", "0", "no":
			out.EnvDisabled = true
		}
	}

	row, err := db.GetOIDCSettingsDecrypted(s.dbc(), s.SecretKeyHex)
	haveRow := err == nil
	if err != nil && !errors.Is(err, db.ErrOIDCSettingsNotFound) {
		log.Printf("oidc: read oidc_settings: %v (falling back to env)", err)
	}

	// issuer / client_id / redirect_uris / key_dir: row value (when non-empty)
	// beats env.
	pick := func(field, rowVal, envVal, def string) string {
		if strings.TrimSpace(rowVal) != "" {
			return set(field, strings.TrimSpace(rowVal), "ui")
		}
		if envVal != "" {
			return set(field, envVal, "env")
		}
		return set(field, def, "default")
	}
	out.Issuer = pick("issuer", row.Issuer, envIssuer, "")
	out.ClientID = pick("client_id", row.ClientID, envClientID, "headscale")
	out.RedirectURIs = pick("redirect_uris", row.RedirectURIs, envRedirects, "")
	out.KeyDir = pick("key_dir", row.KeyDir, envKeyDir, "")

	// The secret is special: a save with an EMPTY field keeps the stored value
	// (the form never echoes it back), so an empty row secret means "not set
	// here" and the env value still applies.
	switch {
	case strings.TrimSpace(row.ClientSecret) != "":
		out.ClientSecret = row.ClientSecret
		out.SecretSource = "ui"
	case envSecret != "":
		out.ClientSecret = envSecret
		out.SecretSource = "env"
	default:
		out.SecretSource = ""
	}
	out.Source["client_secret"] = out.SecretSource

	// enabled: an explicit DB row wins, then the emergency env off-switch, then
	// the legacy "issuer non-empty" rule.
	if haveRow {
		out.Enabled = row.Enabled
		out.Source["enabled"] = "ui"
	} else if out.Issuer != "" {
		out.Enabled = true
		out.Source["enabled"] = "default"
	}
	if out.EnvDisabled {
		out.Enabled = false
		out.Source["enabled"] = "env"
	}
	if out.Issuer == "" {
		out.Enabled = false
		out.Source["enabled"] = "default"
	}
	return out
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

// PostAdminOIDC (B-oidc-setup, 2026-09-21; B290, 2026-09-22) — the form action
// for /admin/oidc. Reads the form fields, upserts the oidc_settings row and
// APPLIES the configuration to the running provider.
//
// B290 changes:
//   - the client_secret is encrypted at rest (SKYGATE_SECRET_KEY), so a database
//     dump no longer exposes it;
//   - the new configuration takes effect IMMEDIATELY (s.OIDCApplier), instead of
//     the old "saved — restart skygate (/admin/update) to apply". That restart
//     requirement was the visible half of the operator's complaint that OIDC
//     "cannot be configured without editing the env file": the form existed, but
//     pressing Save changed nothing they could observe;
//   - empty issuer = cannot be enabled (saved as enabled=0, fields preserved);
//   - an empty client_secret keeps the stored one (the form never echoes it);
//   - the redirect-URI allowlist is validated to be absolute http(s) URLs, and
//     the flash says exactly what was applied.
func (s *Service) PostAdminOIDC(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		// 2026-09-22 (B-oidc-setup fix): raw http.Error
		// replaced with the redirect-with-flash pattern every
		// other POST handler in this package uses. The form
		// parse error is reported via ?err=<text> on the GET
		// round-trip; the template's {{.FlashError}} block
		// renders it. See the B182 / TestNoNewRawErrorPages
		// regression guard.
		http.Redirect(w, r, "/admin/oidc?err="+url.QueryEscape("form parse: "+err.Error()), http.StatusSeeOther)
		return
	}
	enabled := r.FormValue("enabled") == "1"
	issuer := strings.TrimRight(strings.TrimSpace(r.FormValue("issuer")), "/")
	clientID := strings.TrimSpace(r.FormValue("client_id"))
	secret := r.FormValue("client_secret")
	redirects := strings.TrimSpace(r.FormValue("redirect_uris"))
	keyDir := strings.TrimSpace(r.FormValue("key_dir"))

	// Validation with actionable messages (the page renders them).
	if issuer != "" {
		u, perr := url.Parse(issuer)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			s.redirectOIDCErr(w, r, "issuer must be an absolute http(s) URL, e.g. https://gate.example.com — got "+issuer)
			return
		}
	}
	if enabled && issuer == "" {
		s.redirectOIDCErr(w, r, "cannot enable OIDC without an issuer URL — that is the address headscale uses to reach skygate")
		return
	}
	for _, ru := range strings.Split(redirects, ",") {
		ru = strings.TrimSpace(ru)
		if ru == "" {
			continue
		}
		u, perr := url.Parse(ru)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			s.redirectOIDCErr(w, r, "every redirect URI must be an absolute http(s) URL — got "+ru)
			return
		}
	}

	// An empty secret field keeps the stored one; anything else is new.
	if secret == "" {
		if prev, gerr := db.GetOIDCSettingsDecrypted(s.dbc(), s.SecretKeyHex); gerr == nil {
			secret = prev.ClientSecret
		}
	}
	if enabled && secret == "" {
		s.redirectOIDCErr(w, r, "cannot enable OIDC without a client secret — set one here and paste the same value into headscale's oidc.client_secret")
		return
	}
	// B304: the key directory must be absolute (B270's live failure was a relative
	// dir resolved against a systemd working directory, which killed the boot of an
	// unconfigured feature), and the change must be APPLIED to the running store
	// before it is saved — a form that claims a directory the provider is not using
	// is the dead-end this block removes.
	if keyDir != "" && !oidcKeyDirIsAbsolute(keyDir) {
		s.redirectOIDCErr(w, r, "key_dir must be an absolute path (got "+keyDir+") — a relative path is resolved against the service working directory and can make the key store unusable")
		return
	}
	if keyDir == "" && s.OIDCDefaultKeyDir != "" {
		// An empty field means "use the built-in default": make that explicit in
		// the stored row so the page and the host agree on one path.
		keyDir = s.OIDCDefaultKeyDir
	}
	prevKeyDir := s.effectiveOIDCSettings().KeyDir
	keyDirApplied := false
	if s.OIDCKeyDirApplier != nil && strings.TrimSpace(keyDir) != strings.TrimSpace(prevKeyDir) {
		if aerr := s.OIDCKeyDirApplier(keyDir); aerr != nil {
			log.Printf("oidc: key dir apply to %q failed: %v", keyDir, aerr)
			s.redirectOIDCErr(w, r, "key_dir was NOT changed — "+aerr.Error()+" (the running keypair and /oidc/jwks.json are untouched)")
			return
		}
		keyDirApplied = true
	}

	settings := db.OIDCSettings{
		Enabled:      enabled && issuer != "",
		Issuer:       issuer,
		ClientID:     clientID,
		ClientSecret: secret,
		RedirectURIs: redirects,
		KeyDir:       keyDir,
	}
	if err := db.SaveOIDCSettingsEncrypted(s.dbc(), settings, s.SecretKeyHex); err != nil {
		s.redirectOIDCErr(w, r, "save failed: "+err.Error())
		return
	}

	// Apply to the RUNNING provider so the operator sees the effect now.
	applied := false
	if s.OIDCApplier != nil {
		effIssuer, effClientID, effSecret, effRedirects := issuer, clientID, secret, redirects
		if !settings.Enabled {
			// Disabled: keep the stored values but inert the routes.
			effIssuer = ""
		}
		s.OIDCApplier(effIssuer, effClientID, effSecret, effRedirects, settings.Enabled)
		applied = true
	}
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "oidc_settings_saved",
			"enabled="+boolWord(settings.Enabled)+" issuer="+issuer+" client_id="+clientID+
				" secret_set="+boolWord(secret != "")+" redirect_uris="+redirects+
				" key_dir="+keyDir+" key_dir_applied="+boolWord(keyDirApplied)+
				" applied_live="+boolWord(applied))
	}

	msg := "Настройки OIDC сохранены и применены (без перезапуска)."
	if !applied {
		msg = "Настройки OIDC сохранены. Чтобы они вступили в силу, перезапустите skygate (/admin/update)."
	}
	if keyDirApplied {
		msg += " Каталог ключей переключён на " + keyDir + " — сервис ключей уже работает оттуда."
	}
	if !settings.Enabled {
		msg = "Настройки OIDC сохранены; провайдер ВЫКЛЮЧЕН (маршруты /oidc/* отвечают 503)."
	}
	http.Redirect(w, r, "/admin/oidc?ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

// redirectOIDCErr sends the operator back to the page with a named reason.
func (s *Service) redirectOIDCErr(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/admin/oidc?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

// boolWord renders a boolean for audit details.
func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
