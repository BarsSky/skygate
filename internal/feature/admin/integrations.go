// Package admin — integrations.go owns the /admin/integrations
// landing page and the per-component edit forms
// (/admin/derp/config, /admin/headplane).
//
// refactor-v0.30 Phase B step 3b.2 (2026-07-29): moved from
// internal/handlers/admin_integrations.go. The render+probe
// helpers in admin_integrations_renderer.go moved with it
// to feature/admin/integrations_renderer.go (they're only
// used here).

package admin

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
func (s *Service) GetAdminIntegrations(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(s.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/integrations.html", c, map[string]any{
		"Cfg":          cfg,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// ---------- /admin/derp/config ----------

// GetAdminDerpConfig renders the DERP edit form.
func (s *Service) GetAdminDerpConfig(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(s.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/derp_config.html", c, map[string]any{
		"Cfg":              cfg,
		"ExternalURLsText": strings.Join(cfg.DERPExternalURLs, ","),
		"TestResult":       nil,
		"FlashSuccess":     r.URL.Query().Get("ok"),
		"FlashError":       r.URL.Query().Get("err"),
		"FlashInfo":        r.URL.Query().Get("info"),
	})
}

// PostAdminDerpConfig handles the form submit. Three actions:
//
//   action=save (default) — persist the form fields to
//     global_settings. The next GET renders the new state.
//
//   action=apply — save AND push the change to headscale
//     (re-render the config, push via docker cp, SIGHUP).
//
//   action=test — probe each external URL and re-render the
//     page with per-URL test results inline.
func (s *Service) PostAdminDerpConfig(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		derpConfigRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := s.I18n.LangFromRequest(r)
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
		if err := db.SaveIntegrations(s.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		derpConfigRedirect(w, r, i18n.T(lang, "derp.config_saved"), "")
	case "apply":
		if err := db.SaveIntegrations(s.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		s.applyAndRenderDerp(c, cfg, w, r)
	case "test":
		if err := db.SaveIntegrations(s.DB, cfg); err != nil {
			derpConfigRedirect(w, r, "", "Save failed: "+err.Error())
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "derp.config.save",
			fmt.Sprintf("external=%d bundled=%t", len(cfg.DERPExternalURLs), cfg.BundledDERP))
		s.testAndRenderDerp(c, cfg, w, r)
	default:
		derpConfigRedirect(w, r, "", "Unknown action: "+action)
	}
}

func (s *Service) applyAndRenderDerp(c *auth.Claims, cfg *db.IntegrationConfig, w http.ResponseWriter, r *http.Request) {
	full, err := db.LoadIntegrationsFromOS(s.DB)
	if err != nil {
		derpConfigRedirect(w, r, "", "Load full config: "+err.Error())
		return
	}
	full.DERPExternalURLs = cfg.DERPExternalURLs
	full.BundledDERP = cfg.BundledDERP

	rndr := newRendererWithDB(s.DB)
	res := rndr.applyAll(full)
	lang := s.I18n.LangFromRequest(r)
	_ = lang

	trace := strings.Join(res.Steps, " | ")
	s.Backend.Audit(c.UserID, c.Username, "derp.config.apply",
		fmt.Sprintf("ok=%t steps=%q err=%q", res.OK, trace, res.Err))

	loaded, _ := db.LoadIntegrationsFromOS(s.DB)
	s.Backend.RenderWithLayout(w, r, "admin/derp_config.html", c, map[string]any{
		"Cfg":              loaded,
		"ExternalURLsText": strings.Join(loaded.DERPExternalURLs, ","),
		"TestResults":      nil,
		"ApplyResult":      &res,
		"FlashSuccess":     "",
		"FlashError":       "",
		"FlashInfo":        "",
	})
}

func (s *Service) testAndRenderDerp(c *auth.Claims, cfg *db.IntegrationConfig, w http.ResponseWriter, r *http.Request) {
	results := probeAllDerps(cfg.DERPExternalURLs)
	s.Backend.Audit(c.UserID, c.Username, "derp.config.test",
		fmt.Sprintf("tested=%d", len(results)))
	loaded, _ := db.LoadIntegrationsFromOS(s.DB)
	s.Backend.RenderWithLayout(w, r, "admin/derp_config.html", c, map[string]any{
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

func (s *Service) GetAdminHeadplane(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := db.LoadIntegrationsFromOS(s.DB)
	if err != nil {
		http.Error(w, "load integrations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/headplane.html", c, map[string]any{
		"Cfg":          cfg,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

func (s *Service) PostAdminHeadplane(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		headplaneRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := s.I18n.LangFromRequest(r)
	action := r.FormValue("action")
	if action == "" {
		action = "save"
	}

	mode := strings.TrimSpace(r.FormValue("mode"))
	externalURL := strings.TrimSpace(r.FormValue("external_url"))

	switch mode {
	case "bundled", "external", "off":
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

	current, err := db.LoadIntegrationsFromOS(s.DB)
	if err != nil {
		headplaneRedirect(w, r, "", "Load current: "+err.Error())
		return
	}
	current.HeadplaneMode = mode
	current.HeadplaneExternalURL = externalURL
	if err := db.SaveIntegrations(s.DB, current); err != nil {
		headplaneRedirect(w, r, "", "Save failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "headplane.config.save",
		fmt.Sprintf("mode=%s external_url=%q", mode, externalURL))

	if action == "apply" {
		rndr := newRendererWithDB(s.DB)
		res := rndr.applyAll(current)
		trace := strings.Join(res.Steps, " | ")
		s.Backend.Audit(c.UserID, c.Username, "headplane.config.apply",
			fmt.Sprintf("ok=%t steps=%q err=%q", res.OK, trace, res.Err))
		loaded, _ := db.LoadIntegrationsFromOS(s.DB)
		s.Backend.RenderWithLayout(w, r, "admin/headplane.html", c, map[string]any{
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

func derpConfigRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	RedirectWithFlash(w, r, "/admin/derp/config", okMsg, errMsg)
}

func headplaneRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	RedirectWithFlash(w, r, "/admin/headplane", okMsg, errMsg)
}
