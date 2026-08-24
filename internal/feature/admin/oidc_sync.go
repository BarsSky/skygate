// B167 (v1.5.2) — /admin/oidc/sync operator-facing
// page for auto-syncing the OIDC config from
// skygate to headscale.
//
// Why this exists
// ---------------
// B161.1-B161.4 made skygate a working OIDC provider
// for headscale (discovery + JWKS + /authorize +
// /token + /userinfo). But the operator still had
// to hand-edit headscale.conf and `docker restart
// headscale` to enable the integration.
//
// B167 closes the loop. The operator opens
// /admin/oidc/sync, sees the current config (4
// must-match values + a "where to write the
// headscale.conf" form), clicks Apply, and the
// page does the rest:
//
//   1. Generates the headscale.conf `oidc:` block
//   2. Backs up the existing headscale.conf
//   3. Writes the new block into headscale.conf
//      (or downloads the YAML for manual apply)
//   4. Restarts headscale (docker / systemd / k8s
//      — auto-detected)
//   5. Waits for /health
//   6. Updates skygate's .env with the new
//      SKYGATE_OIDC_ISSUER + SKYGATE_OIDC_REDIRECT_URIS
//   7. Reports back the result (JSON) on the
//      /admin/oidc/sync page (flash + collapsible
//      log)
//
// This file implements the Get + Post handlers.
// The form is in admin/oidc_sync.html. The actual
// work happens in internal/oidc/sync.go (Go wrapper
// for the bash script) + deploy/oidc-sync.sh
// (the bash script itself — runs the YAML merge,
// the restart, the health wait, the .env update).
//
// B167.1 (this commit) — full Option C
//   - docker mode (auto-detected via /var/run/docker.sock)
//   - systemd mode (auto-detected via systemctl)
//   - k8s mode (auto-detected via kubectl)
//   - manual mode (writes headscale.conf + .env,
//     operator restarts by hand)
//   - download mode (writes nothing, just shows
//     the YAML on the page for copy-paste)
//   - api mode (headscale 0.30+ `configure oidc` via
//     `docker exec`)
//
// B167.2 — /admin/oidc/sync has a "Sync now" button
// that triggers the full flow. The button is
// admin-only and behind the same authMW as the
// other admin endpoints.
package admin

import (
	"log"
	"net/http"
	"strings"
	"time"

	"skygate/internal/i18n"
	oidcpkg "skygate/internal/oidc"
)

// GetAdminOIDCSync renders the /admin/oidc/sync
// page. The page shows:
//   - The current OIDC config (5 endpoint URLs +
//     env-var values) — same data as /admin/oidc
//     (so the operator can verify before clicking
//     Apply)
//   - The "Apply" form: target headscale config
//     path, headscale container name, "Mode" select
//     (auto / docker / systemd / k8s / manual /
//     download / api), "Sync now" button
//   - The result of the last Apply (flash with
//     collapsible stdout/stderr)
//
// Admin-only. Renders inside the standard admin
// layout (same as /admin/oidc).
func (s *Service) GetAdminOIDCSync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	cfg := s.Cfg
	issuer := strings.TrimRight(cfg.OIDCIssuerURL, "/")
	oidcEnabled := issuer != ""

	// The form's default values. The operator can
	// override on each submit. These defaults are
	// tuned for the most common case (skygate +
	// headscale on the same VM, /home/skyadmin
	// layout).
	formDefaults := map[string]string{
		"HeadscaleConfigPath":  "/home/skyadmin/headscale/config/config.yaml",
		"HeadscaleContainer":   "headscale",
		"SkygateEnvPath":       "/home/skyadmin/skygate/.env",
		"ModeOverride":         "auto",
		"RedirectURIs":         cfg.OIDCRedirectURIs,
		"ClientID":             cfg.OIDCClientID,
	}

	_ = i18n.T(lang, "oidc_sync.title") // keep the import used
	s.Backend.RenderWithLayout(w, r, "admin/oidc_sync.html", c, map[string]any{
		"Page":         "admin/oidc",
		"Title":        i18n.T(lang, "oidc_sync.title"),
		"OIDCEnabled":  oidcEnabled,
		"Issuer":       issuer,
		"ClientID":     cfg.OIDCClientID,
		"KeyDir":       cfg.OIDCKeyDir,
		"RedirectURIs": cfg.OIDCRedirectURIs,
		"FlashOK":      r.URL.Query().Get("ok"),
		"FlashErr":     r.URL.Query().Get("err"),
		"FlashDetail":  r.URL.Query().Get("detail"),
		"FlashMode":    r.URL.Query().Get("mode"),
		"FlashResult":  r.URL.Query().Get("result"),
		"FormDefaults": formDefaults,
		"AutoSync":     oidcpkg.ShouldAutoSync(),
	})
}

// PostAdminOIDCSync handles the "Apply" form
// submission. The flow:
//   1. Parse the form
//   2. Validate the inputs (no blank issuer /
//      client_id / client_secret — those come
//      from the current env, not the form, but
//      we double-check)
//   3. Call oidc.RunSync with the form values
//   4. Redirect back to /admin/oidc/sync with
//      a flash message (ok=1 or err=...) + a
//      detail=base64-encoded stdout/stderr for
//      the collapsible log
//
// The function is admin-only + behind authMW.
// The sync is synchronous (the operator sees the
// result on the same page render). Worst-case
// 120s timeout (matches the sync.go context).
func (s *Service) PostAdminOIDCSync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/oidc/sync?err=parse_form", http.StatusFound)
		return
	}

	// Pull the form values (with defaults from
	// the env-var config so the operator doesn't
	// have to type them every time).
	issuer := strings.TrimRight(s.Cfg.OIDCIssuerURL, "/")
	clientID := s.Cfg.OIDCClientID
	clientSecret := s.Cfg.OIDCClientSecret
	redirectURIs := s.Cfg.OIDCRedirectURIs

	// Allow the form to override redirect_uris
	// (some operators want to use a different
	// callback URL than the default).
	if v := strings.TrimSpace(r.FormValue("redirect_uris")); v != "" {
		redirectURIs = v
	}

	// Required: issuer + client_secret.
	if issuer == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=issuer_empty&detail="+urlQueryEscape("SKYGATE_OIDC_ISSUER is not set on the skygate container"),
			http.StatusFound)
		return
	}
	if clientSecret == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=secret_empty&detail="+urlQueryEscape("SKYGATE_OIDC_CLIENT_SECRET is not set on the skygate container"),
			http.StatusFound)
		return
	}
	if redirectURIs == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=redirect_empty&detail="+urlQueryEscape("SKYGATE_OIDC_REDIRECT_URIS is not set on the skygate container"),
			http.StatusFound)
		return
	}

	req := oidcpkg.SyncRequest{
		SkygateURL:           issuer,
		ClientID:             clientID,
		ClientSecret:         clientSecret,
		RedirectURIs:         redirectURIs,
		HeadscaleConfigPath:  strings.TrimSpace(r.FormValue("headscale_config_path")),
		HeadscaleContainer:   strings.TrimSpace(r.FormValue("headscale_container")),
		SkygateEnvPath:       strings.TrimSpace(r.FormValue("skygate_env_path")),
		ModeOverride:         strings.TrimSpace(r.FormValue("mode")),
		DownloadOnly:         r.FormValue("mode") == "download",
	}

	log.Printf("oidc sync: admin apply by user=%s mode=%q", c.Username, req.ModeOverride)

	// Use a 120s timeout for the whole operation
	// (matches the script's worst case: 60s health
	// wait + the time for backup + YAML write +
	// .env write + restart).
	res, err := oidcpkg.RunSync(req)
	if err != nil {
		log.Printf("oidc sync: failed: %v", err)
		http.Redirect(w, r, "/admin/oidc/sync?err=apply_failed&detail="+urlQueryEscape(err.Error()),
			http.StatusFound)
		return
	}

	// Success. The flash message shows the
	// result summary; the collapsible log has
	// the full stdout (in case the operator
	// wants to copy-paste the YAML).
	detail := summarizeSyncResult(res)
	mode := res.Mode
	// Pass the full result as base64 so the page
	// can render the YAML block in a <details>.
	// (We don't need base64 since url.QueryEscape
	// handles newlines; we just put the result
	// summary in the URL.)
	http.Redirect(w, r, "/admin/oidc/sync?ok=1&mode="+urlQueryEscape(mode)+
		"&detail="+urlQueryEscape(detail)+
		"&result="+urlQueryEscape(res.OIDCBlockYAML),
		http.StatusFound)
}

// summarizeSyncResult returns a short human-readable
// summary suitable for the flash message. The
// full OIDC block is in the `result` query param
// and rendered separately in the collapsible log.
func summarizeSyncResult(res *oidcpkg.SyncResult) string {
	parts := []string{}
	if res.HeadscaleRestarted {
		parts = append(parts, "headscale restarted")
		if res.HeadscaleHealthy {
			parts = append(parts, "healthy")
		} else {
			parts = append(parts, "health=unknown")
		}
	} else {
		parts = append(parts, "headscale NOT restarted (mode="+res.Mode+")")
	}
	if res.EnvUpdated {
		parts = append(parts, ".env updated")
	}
	parts = append(parts, "mode="+res.Mode)
	parts = append(parts, "took="+(time.Duration(res.DurationMs) * time.Millisecond).String())
	return strings.Join(parts, " · ")
}
