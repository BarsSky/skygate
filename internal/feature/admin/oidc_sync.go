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
	"os"
	"regexp"
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
	// B304: the page used to read the RAW env (s.Cfg.OIDC*) — so an operator who
	// had saved everything in the panel still saw «issuer not set» here and
	// concluded that OIDC could only be configured by editing .env. The effective
	// configuration (DB row wins, env is the fallback) is the only answer that
	// matches what /admin/oidc shows and what the provider actually runs.
	eff := s.effectiveOIDCSettings()
	issuer := strings.TrimRight(eff.Issuer, "/")
	oidcEnabled := eff.Enabled && issuer != ""

	// The form's default values. The operator can
	// override on each submit. These defaults are
	// tuned for the most common case (skygate +
	// headscale on the same VM, /home/skyadmin
	// layout).
	formDefaults := map[string]string{
		"HeadscaleConfigPath": s.OIDCSyncDefaultHeadscaleConfig(),
		"HeadscaleContainer":  "headscale",
		"SkygateEnvPath":      "/home/skyadmin/skygate/.env",
		"ModeOverride":        "auto",
		"RedirectURIs":        eff.RedirectURIs,
		"ClientID":            eff.ClientID,
	}

	_ = i18n.T(lang, "oidc_sync.title") // keep the import used
	s.Backend.RenderWithLayout(w, r, "admin/oidc_sync.html", c, map[string]any{
		"Page":         "admin/oidc",
		"Title":        i18n.T(lang, "oidc_sync.title"),
		"OIDCEnabled":  oidcEnabled,
		"Issuer":       issuer,
		"ClientID":     eff.ClientID,
		"KeyDir":       eff.KeyDir,
		"RedirectURIs": eff.RedirectURIs,
		"FlashOK":      r.URL.Query().Get("ok"),
		"FlashErr":     r.URL.Query().Get("err"),
		"FlashDetail":  r.URL.Query().Get("detail"),
		"FlashMode":    r.URL.Query().Get("mode"),
		"FlashResult":  r.URL.Query().Get("result"),
		"FormDefaults": formDefaults,
		"AutoSync":     oidcpkg.ShouldAutoSync(),
		// B304: the page must say where each value came from and whether the env
		// is involved at all, so "configure it in env" stops being the only story.
		"Source":       eff.Source,
		"SecretSet":    eff.ClientSecret != "",
		"SecretSource": eff.SecretSource,
		"EnvHasConfig": eff.EnvHasConfig,
		"EnvDisabled":  eff.EnvDisabled,
		"SavedOnPanel": eff.Source["issuer"] == "ui" || eff.Source["client_secret"] == "ui",
		"KeyDirLive":   liveOIDCKeyDir(s),
		// B304: the one command that carries this configuration to headscale.
		// It fetches the script pinned to THIS build's tag, so what the operator
		// runs matches the panel they are looking at, and it reads the secret
		// from skygate on the host (never through the browser).
		"ApplyScriptCmd": oidcApplyScriptCommand(s.BuildVersion),
	})
}

// oidcApplyScriptCommand (B304) renders the copy-paste command for the headscale
// side. The ref is the running build's tag when it has one (a release build), and
// main otherwise (a source build), so the script always matches the panel.
func oidcApplyScriptCommand(buildVersion string) string {
	ref, _ := displayVersionForUpdate(buildVersion)
	if !oidcReleaseTagRe.MatchString(ref) {
		// A source build ("dev", "vdev", a bare SHA) has no release to pin.
		ref = "main"
	}
	url := "https://raw.githubusercontent.com/BarsSky/skygate/" + ref + "/deploy/skygate-apply-oidc.sh"
	return "curl -fsSL " + url + " -o /tmp/skygate-apply-oidc.sh && sudo bash /tmp/skygate-apply-oidc.sh --dry-run" +
		"   # затем без --dry-run, когда diff вас устроит"
}

// oidcReleaseTagRe matches a released tag ("v1.5.69"); anything else (dev, main, a
// bare SHA) falls back to the main branch so the command always has a real URL.
var oidcReleaseTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+`)

// OIDCSyncDefaultHeadscaleConfig (B304) returns a headscale configuration path to
// pre-fill the sync form with. It prefers a file that actually exists on this host
// (the historical hardcoded /home/skyadmin/headscale/config/config.yaml is only
// right for one of the operator's installs, and a wrong default is a failed sync
// the operator has to debug), and falls back to that historical path.
func (s *Service) OIDCSyncDefaultHeadscaleConfig() string {
	candidates := []string{
		"/etc/headscale/config.yaml",
		"/etc/headscale/config.yml",
		"/etc/headscale/config.hujson",
		"/var/lib/headscale/config.yaml",
		"/home/skyadmin/headscale/config/config.yaml",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return candidates[len(candidates)-1]
}

// liveOIDCKeyDir reports the directory the RUNNING key store uses ("" when the
// applier is not wired), so the sync page can show the same path /admin/oidc does.
func liveOIDCKeyDir(s *Service) string {
	if s == nil || s.OIDCKeyDirFn == nil {
		return ""
	}
	dir, _, _ := s.OIDCKeyDirFn()
	return dir
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

	// Pull the form values from the EFFECTIVE configuration (B304): the DB row
	// saved on /admin/oidc wins, the env is only a fallback. Pre-B304 this read
	// s.Cfg.OIDC* directly, so the sync refused to run for an operator who had
	// configured everything in the panel — with a message telling them to set env
	// vars they did not need ("SKYGATE_OIDC_ISSUER is not set on the skygate
	// container"), which is precisely the «из вебинтерфейса это никак не
	// изменить» complaint from the native host.
	eff := s.effectiveOIDCSettings()
	issuer := strings.TrimRight(eff.Issuer, "/")
	clientID := eff.ClientID
	clientSecret := eff.ClientSecret
	redirectURIs := eff.RedirectURIs

	// Allow the form to override redirect_uris
	// (some operators want to use a different
	// callback URL than the default).
	if v := strings.TrimSpace(r.FormValue("redirect_uris")); v != "" {
		redirectURIs = v
	}

	// Required: issuer + client_secret. The message names the PANEL field to fix
	// (the env var name stays as a parenthetical, because an operator who really
	// does configure this by env needs it).
	if issuer == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=issuer_empty&detail="+urlQueryEscape("заполните Issuer на /admin/oidc и сохраните — env-переменная SKYGATE_OIDC_ISSUER больше не обязательна"),
			http.StatusFound)
		return
	}
	if clientSecret == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=secret_empty&detail="+urlQueryEscape("заполните Client secret на /admin/oidc и сохраните — env-переменная SKYGATE_OIDC_CLIENT_SECRET больше не обязательна"),
			http.StatusFound)
		return
	}
	if redirectURIs == "" {
		http.Redirect(w, r, "/admin/oidc/sync?err=redirect_empty&detail="+urlQueryEscape("заполните Redirect URIs на /admin/oidc и сохраните (обычно https://<headscale-host>/oidc/callback)"),
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
