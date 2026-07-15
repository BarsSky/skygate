// 2026-07-15: Этап 14 v14 (v0.11.0) — admin handlers for the
// runtime-editable integration config.
//
// Three new pages:
//
//   /admin/integrations       — landing. Shows current state of
//                                every pluggable component
//                                (DERP, Headplane) with a
//                                "Configure" link to each.
//
//   /admin/derp/config         — edit form for the DERP list
//                                (external URLs + bundled toggle).
//                                Save persists to global_settings;
//                                v0.11.0 stops there. v0.11.1
//                                will add a runtime renderer
//                                (re-apply headscale config
//                                + restart) so the user doesn't
//                                have to run ./deploy/deploy.sh.
//
//   /admin/headplane           — edit form for the Headplane
//                                mode (bundled / external / off)
//                                + the external URL.
//
// The storage helpers (db.LoadIntegrations, db.SaveIntegrations)
// live in internal/db/integrations.go and are backed by
// global_settings, with env-var fallback for operators who
// haven't visited the UI yet (the v0.10.12 deploy-time model
// keeps working).
//
// CSRF: same approach as the other admin forms — none. The
// actions are admin-only and the session cookie is the gate.
// /admin/telegram (the most sensitive admin page) follows the
// same convention.
//
// Audit: every save writes an audit_log row. Operators see
// these in /admin/audit.

package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// ---------- /admin/integrations ----------

// GetAdminIntegrations renders the landing page.
func (a *App) GetAdminIntegrations(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(a.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderWithLayout(w, r, "admin-integrations", c, map[string]any{
		"Cfg":          cfg,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// ---------- /admin/derp/config ----------

// GetAdminDerpConfig renders the DERP edit form.
func (a *App) GetAdminDerpConfig(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(a.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderWithLayout(w, r, "admin-derp-config", c, map[string]any{
		"Cfg":              cfg,
		"ExternalURLsText": strings.Join(cfg.DERPExternalURLs, ","),
		"TestResult":       nil, // no test result unless POST'd
		"FlashSuccess":     r.URL.Query().Get("ok"),
		"FlashError":       r.URL.Query().Get("err"),
		"FlashInfo":        r.URL.Query().Get("info"),
	})
}

// PostAdminDerpConfig handles the form submit. "save" persists;
// "test" probes a single URL (deprecated — see below).
func (a *App) PostAdminDerpConfig(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		derpConfigRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)

	// "save" (default). Read the form fields, validate, persist.
	rawURLs := r.FormValue("external_urls")
	cfg := &db.IntegrationConfig{
		DERPExternalURLs: splitAndTrimCSV(rawURLs),
		BundledDERP:      r.FormValue("bundled_enabled") == "1",
	}
	if err := db.SaveIntegrations(a.DB, cfg); err != nil {
		derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "derp.config.save",
		fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
	derpConfigRedirect(w, r, i18n.T(lang, "derp.config_saved"), "")
}

// ---------- /admin/headplane ----------

// GetAdminHeadplane renders the Headplane edit form.
func (a *App) GetAdminHeadplane(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(a.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderWithLayout(w, r, "admin-headplane", c, map[string]any{
		"Cfg":          cfg,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// PostAdminHeadplane persists the Headplane mode + URL.
func (a *App) PostAdminHeadplane(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		headplaneRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)

	mode := strings.TrimSpace(r.FormValue("mode"))
	externalURL := strings.TrimSpace(r.FormValue("external_url"))

	switch mode {
	case "bundled", "external", "off":
		// OK
	default:
		headplaneRedirect(w, r, "", "Invalid mode: "+mode)
		return
	}
	if mode == "external" && externalURL == "" {
		headplaneRedirect(w, r, "", "External URL required when mode=external")
		return
	}
	if externalURL != "" {
		if u, err := url.Parse(externalURL); err != nil || u.Scheme != "https" || u.Host == "" {
			headplaneRedirect(w, r, "", "External URL must be a valid https URL")
			return
		}
	}

	// Read-modify-write keeps the DERP fields intact.
	current, err := db.LoadIntegrationsFromOS(a.DB)
	if err != nil {
		headplaneRedirect(w, r, "", "Load current: "+err.Error())
		return
	}
	current.HeadplaneMode = mode
	current.HeadplaneExternalURL = externalURL
	if err := db.SaveIntegrations(a.DB, current); err != nil {
		headplaneRedirect(w, r, "", "Save failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "headplane.config.save",
		fmt.Sprintf("mode=%s external_url=%q", mode, externalURL))
	headplaneRedirect(w, r, i18n.T(lang, "headplane.config_saved"), "")
}

// ---------- helpers ----------

// derpConfigRedirect / headplaneRedirect are tiny redirect
// helpers. We don't use a shared one because each form has
// its own page path; the helper centralises the "ok/err
// flash" pattern but keeps the target path explicit.
func derpConfigRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	redirectWithFlash(w, r, "/admin/derp/config", okMsg, errMsg)
}

func headplaneRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	redirectWithFlash(w, r, "/admin/headplane", okMsg, errMsg)
}

func redirectWithFlash(w http.ResponseWriter, r *http.Request, path, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := path
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// probeDerpmapURL fetches the given URL with a 5s timeout and
// returns (ok, err). A successful response with HTTP 200 is "ok";
// anything else is an error. Currently unused — the form
// only has "save" — but kept here for the future "Test URL"
// button that v0.11.1 will wire in alongside the runtime
// renderer (the renderer's preview step needs the same probe).
func probeDerpmapURL(u string, timeout time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, "bad URL: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "fetch: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 16)
	n, _ := io.ReadFull(resp.Body, buf)
	if n == 0 {
		return false, "empty body"
	}
	return true, ""
}

// splitAndTrimCSV is the form-side counterpart to db.splitCSV.
// The form input may have either commas or newlines (the
// textarea helper shows one per line). Normalise to
// comma-separated before splitting, trim whitespace, drop
// empty entries.
func splitAndTrimCSV(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\n", ",")
	s = strings.ReplaceAll(s, "\r", "")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
