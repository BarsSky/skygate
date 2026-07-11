package handlers

import (
	"database/sql"
	"net/http"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/ratelimit"
	"skygate/internal/telegram"
	"skygate/internal/i18n"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

func init() { i18n.SetGlobal(i18n.New()) }







type App struct {
	Version string
	RateLimiter *ratelimit.Limiter
	Notifier    telegram.Notifier
	I18n         *i18n.Catalog
	DB           *sql.DB
	HS           *headscale.Client
	HeadscaleKey string
	JWTSecret    string
	// ControlURL is the public-facing URL of the headscale control plane,
	// shown to users in preauth instructions so they can configure
	// Tailscale with a custom coordination server. Typically
	// https://head.skynas.ru; falls back to a hardcoded default if the
	// SKYGATE_CONTROL_URL env var is empty at startup.
	ControlURL   string
	SessionHours int
	DerpBaseURL  string // base URL of the local custom DERP server
	SSHKeyPath   string // SSH key for exit node route sync
	Cfg         *config.Config // 2026-07-07: issue #12 — limits & stagger sync

	templates *Templates
}

func New(d *sql.DB, hs *headscale.Client, headscaleKey, secret, controlURL, sshKeyPath string, sessionH int, cfg *config.Config) *App {
	return &App{
		DB:           d,
		HS:           hs,
		HeadscaleKey: headscaleKey,
		JWTSecret:    secret,
		ControlURL:   controlURL,
		SessionHours: sessionH,
		DerpBaseURL:  "http://192.168.13.69:8766",
		templates:    LoadTemplates(),
	Notifier:    telegram.NoopNotifier{},
		I18n:         i18n.New(),
		Cfg:          cfg,
	}
}

// render executes a template directly (no layout). Used for self-contained pages.
func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), 500)
	}
}

// renderWithLayout wraps a fragment template in the layout. data is merged into
// the wrapper, so handlers can add per-page fields (Nodes, Users, Entries, ...).
// IsAdmin and Page are auto-derived from c (the JWT claims) so admin nav stays visible.
func (a *App) renderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	// 2026-07-10: i18n. Detect lang from cookie/Accept-Language, build
	// a Translations object so templates can call {{.T "key"}}.
	lang := a.I18n.LangFromRequest(r)
	data["Lang"] = lang
	data["T"] = &i18n.Translations{Catalog: a.I18n, Lang: lang}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data["Page"] = pageFromName(name)
	if c != nil {
		data["Username"] = c.Username
		data["IsAdmin"] = c.IsAdmin
	}
	// Theme: prefer explicit theme in data, else derive from logged-in user, else linear.
	theme := db.ThemeLinear
	if c != nil {
		theme = db.GetUserTheme(a.DB, c.UserID)
	}
	if t, ok := data["Theme"].(string); ok && db.IsValidTheme(t) {
		theme = t
	}
	data["Theme"] = theme
	data["ThemeLabel"] = db.ThemeLabel(theme)
	data["Version"] = a.Version
	wrapper := map[string]any{
		"Page":         data["Page"],
		"BodyTemplate": name,
		"Title":        a.I18n.T(lang, pageTitle(name)),
		"Theme":        theme,
		"ThemeLabel":   db.ThemeLabel(theme),
	}
	for k, v := range data {
		wrapper[k] = v
	}
	if err := a.templates.ExecuteTemplate(w, "layout", wrapper); err != nil {
		http.Error(w, "render: "+err.Error(), 500)
	}
}

func pageFromName(name string) string {
	name = name[:len(name)-len(".html")]
	if name == "dashboard" {
		return "dashboard"
	}
	if name == "user/devices" || name == "user/preauth_result" {
		return "my/devices"
	}
	if name == "user/exit_nodes" {
		return "my/exit-nodes"
	}
	if strings.HasPrefix(name, "admin/") {
		return name
	}
	if name == "help" {
		return "help"
	}
	return name
}

// pageTitle returns an i18n key (not a translated string) for the given
// template name. The caller (renderWithLayout) resolves it through the
// per-request Translations so the title follows the chosen language.
func pageTitle(name string) string {
	switch name {
	case "dashboard.html":
		return "title.dashboard"
	case "user/devices.html":
		return "title.my_devices"
	case "user/preauth_result.html":
		return "title.preauth"
	case "user/exit_nodes.html":
		return "title.my_exit_nodes"
	case "user/account.html":
		return "title.account"
	case "user/keys.html":
		return "title.my_keys"
	case "user/exit_rules_help.html":
		return "title.exit_rules_help"
	case "my_tokens.html":
		return "title.my_tokens"
	case "admin/users.html":
		return "title.admin_users"
	case "admin/devices.html":
		return "title.admin_devices"
	case "admin/acls.html":
		return "title.admin_acls"
	case "admin/audit.html":
		return "title.admin_audit"
	case "admin/derp.html":
		return "title.admin_derp"
	case "admin/backup.html":
		return "title.admin_backup"
	case "admin/settings.html":
		return "title.admin_settings"
	case "admin/telegram.html":
		return "title.admin_telegram"
	case "admin/exit_rules.html":
		return "title.admin_exit_rules"
	case "admin/exit_rules_cleanup.html":
		return "title.admin_exit_rules_cleanup"
	case "admin/exit_rules_nodes.html":
		return "title.admin_exit_rules_nodes"
	case "admin/exit_nodes.html":
		return "title.admin_exit_nodes"
	case "help.html":
		return "title.help"
	case "exit_rules.html":
		return "title.exit_rules"
	default:
		return "title.skygate"
	}
}

func dataValue(data any, key string) any {
	if m, ok := data.(map[string]any); ok {
		return m[key]
	}
	return nil
}

// currentUser parses JWT cookie and returns claims. nil if not authenticated.
func (a *App) currentUser(r *http.Request) *auth.Claims {
	c, err := r.Cookie("skygate_session")
	if err == nil && c.Value != "" {
		claims, err := auth.ParseJWT(a.JWTSecret, c.Value)
		if err == nil {
			return claims
		}
	}
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") {
		tok := strings.TrimPrefix(authHdr, "Bearer ")
		if tok != "" {
			rows, err := a.DB.Query("SELECT pt.user_id, pu.username, pu.is_admin, pt.token_hash FROM personal_api_tokens pt JOIN portal_users pu ON pu.id = pt.user_id")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var uid int64; var uname string; var adm bool; var hash string
					if rows.Scan(&uid, &uname, &adm, &hash) == nil {
						if auth.CheckAPIToken(hash, tok) {
							rows.Close()
							a.DB.Exec("UPDATE personal_api_tokens SET last_used_at=strftime('%s','now') WHERE token_hash=?", hash)
							return &auth.Claims{UserID: uid, Username: uname, IsAdmin: adm}
						}
					}
				}
			}
		}
	}
	return nil
}

// audit writes a row to the audit log.
func (a *App) audit(userID int64, username, action, detail string) {
	_, _ = a.DB.Exec(`INSERT INTO audit_log(user_id, username, action, detail) VALUES(?,?,?,?)`,
		userID, username, action, detail)
}

// ---------- FILE INDEX ----------
//
// handlers.go owns only shared infra: App struct, render helpers,
// currentUser, audit, getMaxRulesForUser.
//
// All per-feature handlers live in focused siblings:
//   - handlers_settings.go          — /settings/theme (theme switcher)
//   - handlers_help.go              — /help
//   - handlers_my_preauth.go        — POST /my/preauth
//   - handlers_my_exit_nodes.go     — GET  /my/exit-nodes
//   - handlers_my_keys.go           — /my/keys (list + expire)
//   - handlers_my_devices.go        — GET  /my/devices
//   - handlers_dashboard.go         — /dashboard + TailnetMetrics
//   - handlers_auth.go              — login / logout / lang
//   - handlers_my_account.go        — /my/account (password change)
//   - handlers_api_tokens.go        — /my/tokens (API tokens)
//   - handlers_node_ownership.go    — backfillNodeOwnership helper
//   - handlers_admin_users.go       — /admin/users
//   - handlers_admin_nodes.go       — /admin/devices (tag/untag)
//   - handlers_admin_pages.go       — /admin/audit, /admin/acls
//   - handlers_derp.go              — /admin/derp + DERP types
//
// See AGENTS.md "Sister files" for current line counts.

// 2026-07-07: getMaxRulesForUser returns per-user rule limit or default.
func (a *App) getMaxRulesForUser(username string) int {
	if a.Cfg == nil { return 0 }
	if v, ok := a.Cfg.UserMaxRules[username]; ok {
		return v
	}
	return a.Cfg.MaxRulesPerDevice
}
