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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"skygate/internal/auth"
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

// PostAdminDerpConfig handles the form submit. Three actions:
//
//   action=save (default) — persist the form fields to
//     global_settings. The next GET renders the new state.
//     No docker / no headscale interaction. This is the
//     same behaviour as v0.11.0.
//
//   action=apply — save AND push the change to headscale
//     (re-render the config, `docker exec cat`, SIGHUP).
//     v0.11.1. The apply path also starts/stops the bundled
//     derper container to match BundledDERP.
//
//   action=test — probe each external URL via probeDerpURL
//     and re-render the page with per-URL test results
//     inline. The form fields are saved first so the
//     "Test" button can be used after a quick edit. (If
//     the operator only wants to test the URL they're
//     about to type, they can hit Test first; the input
//     is in the form's POST body so it's available
//     without a prior Save.)
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
	action := r.FormValue("action")
	if action == "" {
		action = "save"
	}

	rawURLs := r.FormValue("external_urls")
	cfg := &db.IntegrationConfig{
		DERPExternalURLs: splitAndTrimCSV(rawURLs),
		BundledDERP:      r.FormValue("bundled_enabled") == "1",
	}

	switch action {
	case "save":
		// Persist + redirect (the v0.11.0 behaviour).
		if err := db.SaveIntegrations(a.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		a.audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		derpConfigRedirect(w, r, i18n.T(lang, "derp.config_saved"), "")
	case "apply":
		// Persist first (so the DB is current), then
		// re-render with the apply trace inline. We
		// don't redirect here because the operator
		// needs to see the trace; a redirect would
		// flash the success message but lose the
		// per-step trace.
		if err := db.SaveIntegrations(a.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		a.audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		a.applyAndRenderDerp(c, cfg, w, r)
	case "test":
		// Save first so the test results persist
		// with the form data (if the operator
		// wants to Apply right after testing).
		if err := db.SaveIntegrations(a.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		a.audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		a.testAndRenderDerp(c, cfg, w, r)
	default:
		derpConfigRedirect(w, r, "", "Unknown action: "+action)
	}
}

// applyAndRenderDerp re-renders the headscale config +
// starts/stops the bundled derper, then re-renders the
// DERP config page with the apply trace inline. The
// operator sees the steps in the green / red flash so
// they can see exactly what the renderer did (and
// where it failed, if anything).
func (a *App) applyAndRenderDerp(c *auth.Claims, cfg *db.IntegrationConfig, w http.ResponseWriter, r *http.Request) {
	rndr := newRenderer()
	res := rndr.applyAll(cfg)
	lang := a.I18n.LangFromRequest(r)
	_ = lang

	// Audit log: one row per apply, with the trace
	// attached so the operator can reconstruct the
	// sequence later from /admin/audit.
	trace := strings.Join(res.Steps, " | ")
	a.audit(c.UserID, c.Username, "derp.config.apply",
		fmt.Sprintf("ok=%t steps=%q err=%q", res.OK, trace, res.Err))

	// Re-render the page with the trace inline.
	loaded, _ := db.LoadIntegrationsFromOS(a.DB)
	a.renderWithLayout(w, r, "admin-derp-config", c, map[string]any{
		"Cfg":              loaded,
		"ExternalURLsText": strings.Join(loaded.DERPExternalURLs, ","),
		"TestResults":      nil,
		"ApplyResult":      &res,
		"FlashSuccess":     "",
		"FlashError":       "",
		"FlashInfo":        "",
	})
}

// testAndRenderDerp probes each external DERP URL and
// re-renders the page with the per-URL results inline.
// Each row shows "OK (latency)" or "fail: <reason>".
// The form fields persist so the operator can Apply
// right after testing.
func (a *App) testAndRenderDerp(c *auth.Claims, cfg *db.IntegrationConfig, w http.ResponseWriter, r *http.Request) {
	results := probeAllDerps(cfg.DERPExternalURLs)
	a.audit(c.UserID, c.Username, "derp.config.test",
		fmt.Sprintf("tested=%d", len(results)))
	loaded, _ := db.LoadIntegrationsFromOS(a.DB)
	a.renderWithLayout(w, r, "admin-derp-config", c, map[string]any{
		"Cfg":              loaded,
		"ExternalURLsText": strings.Join(loaded.DERPExternalURLs, ","),
		"TestResults":      results,
		"ApplyResult":      nil,
		"FlashSuccess":     "",
		"FlashError":       "",
		"FlashInfo":        "",
	})
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
// Two actions: "save" (default; v0.11.0 behaviour) and
// "apply" (v0.11.1; saves then starts/stops the headplane
// container to match the new mode). The apply path also
// re-renders the headscale config (headplane mode doesn't
// affect headscale config directly, but applyAll keeps
// the two configs in sync — if an operator saved a
// headplane change earlier and then changed DERP on the
// /admin/derp/config page, the next Apply on either page
// picks up the latest state).
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
	action := r.FormValue("action")
	if action == "" {
		action = "save"
	}

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

	if action == "apply" {
		// Same pattern as the DERP apply path: re-render
		// the page with the apply trace inline so the
		// operator sees what happened.
		rndr := newRenderer()
		res := rndr.applyAll(current)
		trace := strings.Join(res.Steps, " | ")
		a.audit(c.UserID, c.Username, "headplane.config.apply",
			fmt.Sprintf("ok=%t steps=%q err=%q", res.OK, trace, res.Err))
		loaded, _ := db.LoadIntegrationsFromOS(a.DB)
		a.renderWithLayout(w, r, "admin-headplane", c, map[string]any{
			"Cfg":          loaded,
			"ApplyResult":  &res,
			"FlashSuccess": "",
			"FlashError":   "",
		})
		return
	}
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

// probeDerpmapURL used to live here in v0.11.0; v0.11.1
// moved the probe into admin_integrations_renderer.go as
// probeDerpURL (it now returns latency as well as the
// pass/fail flag, which the inline test-results table
// shows to the operator). The test action uses
// probeAllDerps which iterates over the URL list.

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
