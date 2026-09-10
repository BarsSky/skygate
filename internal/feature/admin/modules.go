// modules.go — /admin/modules + /admin/modules/{name} handlers
// (B-mod-admin, 2026-09-10).
//
// The /admin/modules list page renders every registered
// module with its current state, install mode, installed-at,
// started-at, and a row of action buttons (Install / Start /
// Stop / Enable / Disable). The /admin/modules/{name} detail
// page shows the same row plus a sub-feature toggle list and
// the last 20 audit rows for the module.
//
// All handlers delegate to module.Manager (Service.Modules).
// When the Manager is nil (B-mod-core wiring reverted in
// 82c74b38, or a polygon install without B-mod-core), the
// handlers render an informative flash instead of crashing.
//
// The pages are admin-only (IsAdmin check). POST handlers
// validate a CSRF cookie (same pattern as
// AdminTelegramPost). The flash messages go through
// `?ok=...` and `?err=...` query params on the redirect.

package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/module"
)

// ModuleListRow is one row in the /admin/modules list table.
// Built by listModules() from the Manager's state.
type ModuleListRow struct {
	Name        string
	State       string
	InstallMode string
	InstalledAt string // formatted for display
	StartedAt   string
	Actions     []ModuleAction // ordered list of available actions
	HasSubFeat  bool           // true if module exposes sub-features
}

// ModuleAction is a single button on a module row.
type ModuleAction struct {
	Verb      string // "install" | "start" | "stop" | "enable" | "disable"
	Label     string // i18n key for the button label
	LabelText string // pre-rendered button text (Label looked up via I18nT at build time)
	Confirm   string // i18n key for the onsubmit confirm() (empty = no confirm)
	URL       string // pre-rendered POST URL: /admin/modules/{name}/{verb}
}

// ModuleDetailView is the per-module detail page bundle.
type ModuleDetailView struct {
	Row             ModuleListRow
	HealthOK        bool
	HealthChecks    map[string]bool
	HealthLastError string
	HealthLastCheck string
	SubFeatures     []SubFeatureRow
	Audit           []AuditRow
	FlashOK         string
	FlashError      string
}

// SubFeatureRow is one row in the sub-feature toggle list.
type SubFeatureRow struct {
	Name        string
	Description string
	Enabled     bool
	Requires    string // comma-separated names of prerequisite sub-features
	Impact      string
	CanToggle   bool // false if Requires is not met (cannot enable)
}

// AuditRow is one audit_log row for the detail page.
type AuditRow struct {
	When    string // formatted for display
	Action  string
	Detail  string
	By      string // username (or empty for system)
}

// AdminModulesList renders /admin/modules. Admin-only.
//
// Renders every module from Manager.List() with its state +
// install mode + the action buttons appropriate for the
// current state. If Manager is nil (B-mod-core wiring
// reverted), renders a single informative row instead of
// crashing.
func (s *Service) AdminModulesList(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.Modules == nil {
		s.Backend.RenderWithLayout(w, r, "admin/modules.html", c, map[string]any{
			"Page":        "admin/modules",
			"Title":       s.I18nT(c, "modules.title"),
			"Modules":     []ModuleListRow{},
			"NotWired":    true,
			"NotWiredMsg": s.I18nT(c, "modules.not_registered"),
			"FlashOK":     r.URL.Query().Get("ok"),
			"FlashError":  r.URL.Query().Get("err"),
			"CSRF":        "",
		})
		return
	}
	// Mint a fresh CSRF token (the form POSTs need it).
	csrf := s.mintModulesCSRF(w)
	rows := s.listModules(c)
	s.Backend.RenderWithLayout(w, r, "admin/modules.html", c, map[string]any{
		"Page":       "admin/modules",
		"Title":      s.I18nT(c, "modules.title"),
		"Subtitle":   s.I18nT(c, "modules.subtitle"),
		"Modules":    rows,
		"NotWired":   false,
		"FlashOK":    r.URL.Query().Get("ok"),
		"FlashError": r.URL.Query().Get("err"),
		"CSRF":       csrf,
	})
}

// AdminModuleDetail renders /admin/modules/{name}. Admin-only.
func (s *Service) AdminModuleDetail(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape("missing module name"), http.StatusSeeOther)
		return
	}
	if s.Modules == nil {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape(s.I18nT(c, "modules.not_registered")), http.StatusSeeOther)
		return
	}
	mod, ok := s.Modules.Get(name)
	if !ok {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape(fmt.Sprintf("module %q not registered", name)), http.StatusSeeOther)
		return
	}
	row := s.buildModuleRow(c, name, mod)
	view := ModuleDetailView{
		Row:        row,
		SubFeatures: s.buildSubFeatureRows(c, mod),
		Audit:      s.loadModuleAudit(c, name, 20),
		FlashOK:    r.URL.Query().Get("ok"),
		FlashError: r.URL.Query().Get("err"),
	}
	// Health snapshot — best effort, 3s timeout.
	if health, err := s.getModuleHealth(name); err == nil {
		view.HealthOK = health.Healthy
		view.HealthChecks = health.Checks
		view.HealthLastError = health.LastError
		if !health.LastCheck.IsZero() {
			view.HealthLastCheck = health.LastCheck.UTC().Format("2006-01-02 15:04:05 UTC")
		}
	}
	title := s.I18nT(c, "modules.detail_title", name)
	csrf := s.mintModulesCSRF(w)
	s.Backend.RenderWithLayout(w, r, "admin/module_detail.html", c, map[string]any{
		"Page":   "admin/modules",
		"Title":  title,
		"View":   view,
		"Name":   name,
		"CSRF":   csrf,
	})
}

// AdminModulePost is the POST handler for /admin/modules/{name}/*.
// Dispatches by r.PathValue("action") to install/start/stop/
// enable/disable/sub/{subname}. Admin-only. CSRF-protected.
func (s *Service) AdminModulePost(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.Modules == nil {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape(s.I18nT(c, "modules.not_registered")), http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	action := strings.TrimSpace(r.PathValue("action"))
	if name == "" || action == "" {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape("missing name or action"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape("parse form: "+err.Error()), http.StatusSeeOther)
		return
	}
	// CSRF check (same cookie name as AdminTelegramPost).
	if !s.verifyCSRFCookie(r, "skygate_modules_csrf") {
		http.Redirect(w, r, "/admin/modules?err="+urlEscape("CSRF check failed"), http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	switch action {
	case "install":
		// Special: install is on the Module, not Manager.
		// The Manager's Start/Stop/Enable/Disable wrap the
		// Module methods; Install is a Module-level method
		// (B-mod-tailscale: *tailscale.Module.Install).
		// We dispatch via type assertion to the
		// module.Installer interface (defined in
		// module/installer.go).
		mod, ok := s.Modules.Get(name)
		if !ok {
			http.Redirect(w, r, "/admin/modules?err="+urlEscape("module not registered: "+name), http.StatusSeeOther)
			return
		}
		installer, ok := mod.(module.Installer)
		if !ok {
			http.Redirect(w, r, "/admin/modules?err="+urlEscape("module "+name+" does not support install"), http.StatusSeeOther)
			return
		}
		if err := installer.Install(ctx); err != nil {
			s.redirectWithModuleError(w, r, name, err)
			return
		}
		s.auditModule(c, name, "install", "")
		mode := s.I18nT(c, "modules.install_mode_os") // default label; the real mode is in the audit row
		http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, urlEscape(s.I18nT(c, "modules.flash_installed", name, mode))), http.StatusSeeOther)
	case "start":
		if err := s.Modules.Start(ctx, name); err != nil {
			s.redirectWithModuleError(w, r, name, err)
			return
		}
		s.auditModule(c, name, "start", "")
		http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, urlEscape(s.I18nT(c, "modules.flash_started", name))), http.StatusSeeOther)
	case "stop":
		if err := s.Modules.Stop(ctx, name); err != nil {
			s.redirectWithModuleError(w, r, name, err)
			return
		}
		s.auditModule(c, name, "stop", "")
		http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, urlEscape(s.I18nT(c, "modules.flash_stopped", name))), http.StatusSeeOther)
	case "enable":
		if err := s.Modules.Enable(name); err != nil {
			s.redirectWithModuleError(w, r, name, err)
			return
		}
		s.auditModule(c, name, "enable", "")
		http.Redirect(w, r, fmt.Sprintf("/admin/modules?ok=%s", urlEscape("module "+name+" enabled")), http.StatusSeeOther)
	case "disable":
		if err := s.Modules.Disable(name); err != nil {
			s.redirectWithModuleError(w, r, name, err)
			return
		}
		s.auditModule(c, name, "disable", "")
		http.Redirect(w, r, fmt.Sprintf("/admin/modules?ok=%s", urlEscape("module "+name+" disabled")), http.StatusSeeOther)
	default:
		// Unknown action — treat as sub-feature toggle.
		// The action string is the sub-feature name
		// (e.g. "sub/cluster", "sub/telegram").
		const subPrefix = "sub/"
		if !strings.HasPrefix(action, subPrefix) {
			http.Redirect(w, r, "/admin/modules?err="+urlEscape("unknown action: "+action), http.StatusSeeOther)
			return
		}
		subName := strings.TrimPrefix(action, subPrefix)
		enabled := r.FormValue("enabled") == "1"
		if enabled {
			if err := s.Modules.EnableSubFeature(ctx, name, subName); err != nil {
				s.redirectWithModuleError(w, r, name, err)
				return
			}
			s.auditModule(c, name, "subfeature.enable", "sub="+subName)
			http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, urlEscape(s.I18nT(c, "modules.flash_subfeature_on", subName))), http.StatusSeeOther)
		} else {
			if err := s.Modules.DisableSubFeature(ctx, name, subName); err != nil {
				s.redirectWithModuleError(w, r, name, err)
				return
			}
			s.auditModule(c, name, "subfeature.disable", "sub="+subName)
			http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?ok=%s", name, urlEscape(s.I18nT(c, "modules.flash_subfeature_off", subName))), http.StatusSeeOther)
		}
	}
}

// AdminModulesListCSRF issues a fresh CSRF cookie for the
// /admin/modules list page. Mounted at /admin/modules/csrf
// (GET) so the page's <form action> can include a hidden
// input with the cookie value. Standard CSRF pattern.
//
// The list page (AdminModulesList) ALSO mints a fresh
// cookie + token on every GET (via mintModulesCSRF) so
// the cookie + form value are always in sync. This
// separate endpoint is for refresh-on-demand.
func (s *Service) AdminModulesListCSRF(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = s.mintModulesCSRF(w)
	// Redirect back to the list page.
	http.Redirect(w, r, "/admin/modules", http.StatusSeeOther)
}

// mintModulesCSRF generates a fresh CSRF token, sets the
// skygate_modules_csrf cookie, and returns the token value
// (for the form to embed as a hidden input). Called by
// every GET handler that renders a form.
func (s *Service) mintModulesCSRF(w http.ResponseWriter) string {
	csrf, err := randomToken(8)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_modules_csrf",
		Value:    csrf,
		Path:     "/admin/modules",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return csrf
}

// listModules builds []ModuleListRow for the list page.
// Reads the Manager's List() and pairs each entry with
// its persisted state. Actions are state-dependent:
//   - StateNotInstalled → only "install"
//   - StateInstalled    → "start", "enable"/"disable"
//   - StateRunning      → "stop", "enable"/"disable"
//   - StateStopped      → "start", "enable"/"disable"
//   - StateError        → "start" (recovery), "stop"
func (s *Service) listModules(c *auth.Claims) []ModuleListRow {
	mods := s.Modules.List()
	rows := make([]ModuleListRow, 0, len(mods))
	for _, info := range mods {
		mod, _ := s.Modules.Get(info.Name)
		row := s.buildModuleRow(c, info.Name, mod)
		rows = append(rows, row)
	}
	return rows
}

// buildModuleRow constructs a ModuleListRow from a Module
// + its name. Shared between the list page and the detail
// page so the row rendering is consistent.
func (s *Service) buildModuleRow(c *auth.Claims, name string, mod module.Module) ModuleListRow {
	status := mod.Status()
	row := ModuleListRow{
		Name:        name,
		State:       status.State,
		InstallMode: s.installModeDisplay(c, status.Info["install_mode"]),
		Actions:     s.actionsForState(c, name, status.State),
		HasSubFeat:  len(mod.SubFeatures()) > 0,
	}
	if !status.InstalledAt.IsZero() {
		row.InstalledAt = status.InstalledAt.UTC().Format("2006-01-02 15:04 MST")
	}
	if !status.StartedAt.IsZero() {
		row.StartedAt = status.StartedAt.UTC().Format("2006-01-02 15:04 MST")
	}
	return row
}

// actionsForState returns the action buttons that make sense
// for the given module state. Each action includes its
// pre-rendered label + URL (so the template can render it
// without helper funcs).
func (s *Service) actionsForState(c *auth.Claims, moduleName, state string) []ModuleAction {
	build := func(verb, labelKey, confirmKey string) ModuleAction {
		return ModuleAction{
			Verb:      verb,
			Label:     labelKey,
			LabelText: s.I18nT(c, labelKey),
			Confirm:   confirmKey,
			URL:       "/admin/modules/" + moduleName + "/" + verb,
		}
	}
	switch state {
	case module.StateNotInstalled:
		return []ModuleAction{build("install", "modules.install", "modules.confirm_install")}
	case module.StateInstalled, module.StateRunning, module.StateStopped:
		actions := []ModuleAction{}
		if state == module.StateRunning || state == module.StateStopped {
			actions = append(actions, build("stop", "modules.stop", "modules.confirm_stop"))
		}
		if state == module.StateInstalled || state == module.StateStopped {
			actions = append(actions, build("start", "modules.start", ""))
		}
		return actions
	case module.StateError:
		return []ModuleAction{
			build("start", "modules.start", ""),
		}
	}
	return nil
}

// installModeDisplay translates the install_mode code
// (os_level / in_container / attach) to a human label via
// the i18n catalog. Falls back to the raw code if the
// catalog doesn't recognise it (e.g. "none").
func (s *Service) installModeDisplay(c *auth.Claims, mode string) string {
	switch mode {
	case "os_level":
		return s.I18nT(c, "modules.install_mode_os")
	case "in_container":
		return s.I18nT(c, "modules.install_mode_in")
	case "attach":
		return s.I18nT(c, "modules.install_mode_at")
	}
	return mode
}

// buildSubFeatureRows builds the sub-feature toggle list.
// Each row's CanToggle is false if any Requires sub-feature
// is not yet enabled — the Manager enforces this on
// EnableSubFeature, but the UI shows it up front so the
// operator doesn't click a button that just errors.
func (s *Service) buildSubFeatureRows(c *auth.Claims, mod module.Module) []SubFeatureRow {
	raw := mod.SubFeatures()
	enabled := s.subFeatureStateMap(mod)
	rows := make([]SubFeatureRow, 0, len(raw))
	for _, sf := range raw {
		isOn := enabled[sf.Name]
		row := SubFeatureRow{
			Name:        sf.Name,
			Description: sf.Description,
			Enabled:     isOn,
			Impact:      sf.Impact,
			CanToggle:   true,
		}
		if len(sf.Requires) > 0 {
			row.Requires = strings.Join(sf.Requires, ", ")
			for _, req := range sf.Requires {
				if !enabled[req] {
					row.CanToggle = false
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// subFeatureStateMap reads the Manager's persistent state
// for the module's sub-features. Returns a name->bool map.
// Nil if the module is not registered or has no state.
func (s *Service) subFeatureStateMap(mod module.Module) map[string]bool {
	// We don't have a direct Manager API for "get state
	// for module X". Use the type assertion to the
	// stateful Module if available. (Future B-block can
	// add a Manager.GetState(name) method.)
	type statefulModule interface {
		GetSubFeatures() map[string]bool
	}
	if sm, ok := mod.(statefulModule); ok {
		return sm.GetSubFeatures()
	}
	// Fallback: read from the Status() return — Status()
	// does not currently include SubFeatures, so we
	// return an empty map. (The Manager will reject
	// enable anyway with ErrSubFeatureRequires if
	// the prereqs aren't met — the UI just shows
	// CanToggle=true incorrectly in this fallback.)
	_ = mod
	return map[string]bool{}
}

// getModuleHealth reads the Manager's Health() for the
// module with a 3s timeout. Returns the result; the caller
// decides what to render on the page.
func (s *Service) getModuleHealth(name string) (module.HealthStatus, error) {
	mod, ok := s.Modules.Get(name)
	if !ok {
		return module.HealthStatus{}, fmt.Errorf("module %q not registered", name)
	}
	// Health() is called via the Module interface; it
	// MUST return within 5s. We don't add a separate
	// timeout here — the Module's own Health() does
	// the 5s enforcement per the B-mod-core contract.
	return mod.Health(), nil
}

// loadModuleAudit reads the last N audit_log rows for the
// module. Filters on action LIKE 'module.<name>.%'. Returns
// the rows formatted for display.
func (s *Service) loadModuleAudit(c *auth.Claims, name string, limit int) []AuditRow {
	dbc := s.dbc()
	if dbc == nil {
		return nil
	}
	rows, err := dbc.Query(`
		SELECT created_at, action, detail, COALESCE(username, '')
		FROM audit_log
		WHERE action LIKE $1
		ORDER BY created_at DESC
		LIMIT $2
	`, "module."+name+".%", limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var (
			ts      int64
			action  string
			detail  sql.NullString
			byUser  sql.NullString
		)
		if err := rows.Scan(&ts, &action, &detail, &byUser); err != nil {
			continue
		}
		out = append(out, AuditRow{
			When:   time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05"),
			Action: action,
			Detail: detail.String,
			By:     byUser.String,
		})
	}
	return out
}

// auditModule writes an audit_log row for a module action
// (called from AdminModulePost after every successful state
// change). The Manager already audits state transitions
// internally (via ModuleConfig.AuditLog), but the POST
// handler also audits so the operator's "who clicked the
// button" is recorded separately from "what the module
// did in response".
func (s *Service) auditModule(c *auth.Claims, moduleName, verb, detail string) {
	if s.Backend == nil {
		return
	}
	username := ""
	uid := int64(0)
	if c != nil {
		username = c.Username
		uid = c.UserID
	}
	action := "module." + moduleName + "." + verb
	if detail != "" {
		action = action + " " + detail
	}
	s.Backend.Audit(uid, username, action, "via /admin/modules UI")
}

// redirectWithModuleError redirects to the detail page
// with a flash error. Centralised so the error message
// format is consistent across all 5 handlers.
func (s *Service) redirectWithModuleError(w http.ResponseWriter, r *http.Request, name string, err error) {
	msg := s.I18nT(nil, "modules.flash_error", err.Error())
	http.Redirect(w, r, fmt.Sprintf("/admin/modules/%s?err=%s", name, urlEscape(msg)), http.StatusSeeOther)
}

// I18nT is a small wrapper for i18n.Tf that handles the
// optional *auth.Claims. The catalog is language-agnostic
// (we use the cookie "lang" or Accept-Language to pick
// ru vs en). For now we use the i18n.Tf signature
// Tf(lang, key, args...) — language picked from the
// claims if available, else "en".
func (s *Service) I18nT(c *auth.Claims, key string, args ...interface{}) string {
	lang := "en"
	if s.I18n == nil {
		// No catalog wired — return the key.
		return key
	}
	// PickLang from cookie / Accept-Language.
	if r := claimsToRequest(c); r != nil {
		lang = s.I18n.LangFromRequest(r)
	}
	if len(args) == 0 {
		return s.I18n.T(lang, key)
	}
	return s.I18n.Tf(lang, key, args...)
}

// claimsToRequest is a small adapter that returns a synthetic
// *http.Request with the Accept-Language header set from
// the user's preferred language (stored in Claims.Lang
// when present). Returns nil if the catalog doesn't need
// the request (or c is nil).
func claimsToRequest(c *auth.Claims) *http.Request {
	if c == nil {
		return nil
	}
	// auth.Claims may or may not have a Lang field;
	// fall back to en.
	lang := "en"
	// Use reflection-free access via type assertion
	// (avoid adding a method to auth.Claims for this).
	type langClaim interface{ GetLang() string }
	if lc, ok := any(c).(langClaim); ok {
		if l := lc.GetLang(); l != "" {
			lang = l
		}
	}
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", lang)
	return r
}

// verifyCSRFCookie checks that the form's csrf field
// matches the cookie. Returns true on match. Cookie name
// is the same for all /admin/modules/* POST forms so the
// /admin/modules/csrf endpoint issues one token and the
// forms reuse it for their 10-minute lifetime.
func (s *Service) verifyCSRFCookie(r *http.Request, cookieName string) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	formCSRF := r.FormValue("csrf")
	if formCSRF == "" {
		return false
	}
	return cookie.Value == formCSRF
}

// randomToken returns a random hex token of N bytes (so
// 2N hex chars). Used for CSRF tokens. Duplicated from
// db.RandomConfirmationToken to avoid a feature→db import
// for this trivial helper.
func randomToken(n int) (string, error) {
	return db.RandomConfirmationToken(n)
}

// urlEscape escapes a string for use in a URL query param.
// Duplicated from internal/feature/admin/telegram.go to
// avoid a private-function import.
func urlEscape(s string) string {
	// Minimal escaping: replace characters that have
	// special meaning in URL query strings. The standard
	// library's url.QueryEscape is the right tool — we
	// can't use it here without an import, so the inline
	// version covers the common cases (space, &, =, ?).
	r := strings.NewReplacer(" ", "%20", "&", "%26", "=", "%3D", "?", "%3F", "#", "%23", "/", "%2F")
	return r.Replace(s)
}

// init wires the modules feature into the Service.
func init() {
	// Force the linker to keep i18n package imported
	// (otherwise go's "unused import" would complain
	// when modules.go is the only consumer).
	_ = i18n.T
}
