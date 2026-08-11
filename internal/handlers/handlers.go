package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/controlplane"
	"skygate/internal/db"
	"skygate/internal/expirewatch"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/httputil"
	"skygate/internal/i18n"
	"skygate/internal/ratelimit"
	"skygate/internal/release"
	"skygate/internal/sidecar"
	"skygate/internal/telegram"
)

func init() { i18n.SetGlobal(i18n.New()) }







type App struct {
	Version string
	// v0.26.0 — process-wide liveness/readiness fields.
	// Set once at boot, read by the /healthz and /readyz
	// handlers. Carried in the App so the handlers can
	// render them without reaching into the global
	// state.
	InstanceID  string        // SKYGATE_INSTANCE_ID env, "unconfigured" if empty
	BuildVersion string        // "v0.26.0" + commit SHA (set by main.go at boot)
	StartedAt   time.Time     // wall-clock when main() returned from setup
	RateLimiter *ratelimit.Limiter
	Notifier    telegram.Notifier
	I18n         *i18n.Catalog
	DB           *sql.DB
	hs           *headscale.Client
	HS           *headscale.Client
	HeadscaleKey string
	JWTSecret    string
	// ControlURL is the public-facing URL of the headscale control plane,
	// shown to users in preauth instructions so they can configure
	// Tailscale with a custom coordination server. Typically
	// https://head.example.com; falls back to a hardcoded default if the
	// SKYGATE_CONTROL_URL env var is empty at startup.
	ControlURL   string
	SessionHours int
	DerpBaseURL  string // base URL of the local custom DERP server
	SSHKeyPath   string // SSH key for exit node route sync
	Cfg         *config.Config // 2026-07-07: issue #12 — limits & stagger sync
	// 2026-07-15: v0.10.12 — public URL of an existing Headplane
	// instance (HEADPLANE_EXTERNAL_URL). When set, the admin
	// ACL page links to this URL instead of the bundled
	// sidecar. Empty = use the bundled sidecar at
	// https://${ControlURL-host}:50445/admin/.
	HeadplaneExternalURL string
	SecretKeyHex string
	// refactor-v0.30 Phase D3 (2026-07-29): the
	// per-user control plane routing (HSForUser,
	// HSGlobal, PlaneURLForUser, InvalidateHSCache)
	// moved to internal/controlplane/router.go. The
	// App still owns a *Router for the per-request
	// routing; the cache + mutex moved into the
	// Router type itself.
	Router *controlplane.Router

	// 2026-07-15: v0.14.0 — release-monitor reference. The
	// /dashboard banner reads ReleaseMonitor.Snapshot() to
	// surface "newer version available" without waiting for
	// the operator to read the Telegram alert. Set by
	// cmd/skygate/main.go after the monitor's Start()
	// returns. nil if the monitor is disabled (the
	// operator ran skygate with SKYGATE_RELEASE_MONITOR=off
	// — not yet a real env var, but the test suite
	// disables it via this nil field).
	ReleaseMonitor *release.Monitor
	// HeadscaleUpdateMonitor (v0.20.0) tracks new
	// headscale releases. The /admin/headscale page reads
	// its Snapshot(); the /admin/exit-nodes page reads
	// UpdateAvailable + BreakingAvailable to render a
	// banner; the bot /headscale command reads the same
	// fields. nil-safe: handlers guard with
	// `if a.HeadscaleUpdateMonitor != nil`.
	HeadscaleUpdateMonitor *headscale_version.Monitor
	// Sidecar is the per-user subnet auto-approver (v0.16.7).
	// The admin /admin/users/{id}/subnet page uses it to issue
	// preauth keys; the bot /mysubnet command uses it for the
	// same; the background goroutine started in cmd/skygate/main.go
	// calls Run() which periodically approves routes + flips
	// status active/disabled based on headscale state.
	Sidecar *sidecar.Manager

	// 2026-07-21: v0.23.3 — node-expiry watcher. The
	// background goroutine started in cmd/skygate/main.go
	// calls Run() which periodically (every
	// cfg.ExpireWatchInterval, default 5m) walks every
	// non-tagged node in headscale and extends any whose
	// Expiry is within cfg.ExpireWatchThreshold
	// (default 7d) out to cfg.ExpireWatchRenewal
	// (default 30d). Works around the Tailscale 1.98.x
	// client behaviour of sending a 2-4-second Expiry
	// in RegisterRequest — see
	// internal/expirewatch/manager.go for the full
	// background. nil if the watcher is disabled
	// (SKYGATE_EXPIREWATCH_ENABLED=false).
	ExpireWatch *expirewatch.Manager

	// 2026-07-15: v0.12.0.2 — Telegram probe result cache.
	// The probe does a real GET to api.telegram.org with a
	// 5s timeout. On the production VM that host is
	// unreachable (RF block + no relay advertised for the
	// resolved IPs), so every page load took the full 5s
	// timeout. The cache holds the most recent result for
	// telegramProbeTTL so the page renders instantly on
	// subsequent loads. Invalidated by the save/rotate/
	// disable/strict handlers so the operator sees the
	// fresh result after they take an action.
	//
	// refactor-v0.30 Phase B step 3b.1a (2026-07-29): the
	// telegram probe cache state (telegramProbeMu,
	// telegramProbeResult, telegramProbeAt,
	// telegramProbeTokenFP) was moved to the Service in
	// internal/feature/admin/. The cache is owned by the
	// feature that uses it now.

	templates *Templates

	// adminSvc is set by main.go after New(). It owns the
	// admin routes that were moved to internal/feature/admin/
	// in refactor-v0.30 Phase B step 3. The thin wrapper
	// methods below (AdminTelegram, AdminTelegramPost) keep
	// the existing /admin/telegram routes working without
	// touching every test caller. New code should call
	// adminSvc directly via the route registration in
	// cmd/skygate/main.go.
	adminSvc adminSvcHandle

	// refactor-v0.30 Phase B step 4 (2026-07-29): the
	// exit_rules feature service. Set by main.go after
	// New() and after exitRulesSvc is constructed. The
	// *App.SyncAdvertisedRoutes and *App.RunDomainAutoUpdater
	// wrappers below route through this field so the
	// /admin/exit-nodes/sync button (called via app.SyncAdvertisedRoutes)
	// and the boot-time autoupdater goroutine
	// (go app.RunDomainAutoUpdater(ctx, ...)) work without
	// duplicating the implementation in handlers/.
	exitRulesSvc exitRulesRunner
}

// adminSvcHandle is an interface so handlers.go doesn't import
// internal/feature/admin (which would create a cyclic dep —
// feature/admin imports handlers via Backend). The concrete
// *adminsvc.Service satisfies it. Set via SetAdminService in
// main.go.
//
// Only the methods that existing tests still call through
// *App live here. New routes go directly to adminSvc in
// main.go.
type adminSvcHandle interface {
	AdminTelegram(w http.ResponseWriter, r *http.Request)
	AdminTelegramPost(w http.ResponseWriter, r *http.Request)
}

// exitRulesRunner is the surface the legacy *App needs from
// the exit_rules feature service. The concrete
// *exitrules.Service satisfies it. Set via SetExitRulesService
// in main.go.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): previously
// these methods lived directly on *App (in exit_rules_sync.go).
// The Service now owns the implementation; *App keeps
// thin wrappers so admin/exit_nodes/sync + the boot-time
// autoupdater goroutine can call into them without holding
// a *Service reference.
type exitRulesRunner interface {
	SyncAdvertisedRoutes() map[string]string
	DomainAutoUpdater() (added, removed int, err error)
	StaggeredSync()
	GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error)
}

// SetAdminService wires the admin feature service into the
// legacy *App so the thin wrapper methods (and any future
// test-time helpers) can route through it. Called once
// from main.go after New() and after adminSvc is constructed.
func (a *App) SetAdminService(s adminSvcHandle) {
	a.adminSvc = s
}

// SetExitRulesService wires the exit_rules feature service
// into the legacy *App so the SyncAdvertisedRoutes +
// RunDomainAutoUpdater wrappers can route through it.
// Called once from main.go after New() and after
// exitRulesSvc is constructed.
func (a *App) SetExitRulesService(s exitRulesRunner) {
	a.exitRulesSvc = s
}

// SyncAdvertisedRoutes is the legacy *App entry point used
// by the /admin/exit-nodes/sync button and the bot. With
// Phase B step 4 it became a thin wrapper that routes
// through the exit_rules feature service.
func (a *App) SyncAdvertisedRoutes() map[string]string {
	if a.exitRulesSvc != nil {
		return a.exitRulesSvc.SyncAdvertisedRoutes()
	}
	return map[string]string{"error": "exit_rules service not wired"}
}

// GenerateRouteSetupScript is the legacy *App entry point
// used by /my/exit-rules (the ?script= download). With
// Phase B step 4c it became a thin wrapper that routes
// through the exit_rules feature service. The form_my.go
// handler still lives in handlers/ (it moves to feature/
// exit_rules/ in step 4e) so the wrapper is preserved
// until then.
func (a *App) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	if a.exitRulesSvc != nil {
		return a.exitRulesSvc.GenerateRouteSetupScript(userID, deviceID, os, restore)
	}
	return "", fmt.Errorf("exit_rules service not wired")
}

// RunDomainAutoUpdater is the boot-time goroutine entry
// point. main.go calls `go app.RunDomainAutoUpdater(ctx,
// cfg.DNSAutoCheck)` once at startup; the wrapper routes
// through the exit_rules feature service.
func (a *App) RunDomainAutoUpdater(ctx context.Context, interval time.Duration) {
	if a.exitRulesSvc == nil {
		return
	}
	if interval <= 0 {
		return
	}
	log.Printf("autoupdater: starting (interval=%s)", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	// 2026-08-06 v0.33.1.18 — read the DB-backed toggle on every
	// tick so the /admin/system_tests UI takes effect without a
	// skygate restart. The env var SKYGATE_DNS_AUTOUPDATE_ENABLED
	// is the default only when the global_settings row doesn't
	// exist (e.g. fresh start). After the first UI toggle the
	// DB value wins.
	checkEnabled := func() bool {
		// 2026-08-06 v0.33.1.18 — read the DB-backed toggle.
		// GetGlobalSetting returns the third arg as the default
		// when the row doesn't exist (first start). We pass ""
		// so missing row → "". Then we map the string to a bool
		// (only "1"/"true" count as enabled). Any other value
		// (including "" after a fresh DB write, or "0"/"false"
		// after the operator disabled it) means the autoupdater
		// is off. The empty-row case here means "operator never
		// touched the UI, defer to the start-up gate in main.go"
		// — and since we ARE running, the start-up gate decided
		// we should be running, so default-true is the right
		// fallback.
		row, err := db.GetGlobalSetting(a.DB, "dns_autoupdate_enabled", "")
		if err != nil {
			log.Printf("autoupdater: read global_settings: %v (assuming enabled)", err)
			return true
		}
		if row == "" {
			return true // no UI override yet — start-up gate decided
		}
		return row == "1" || row == "true"
	}
	// Run once immediately, then on tick.
	if !checkEnabled() {
		log.Printf("autoupdater: dns_autoupdate_enabled=false, skipping initial run")
	} else {
		added, removed, err := a.exitRulesSvc.DomainAutoUpdater()
		if err != nil {
			log.Printf("autoupdater: initial: %v", err)
		} else if added > 0 || removed > 0 {
			log.Printf("autoupdater: initial: added=%d removed=%d", added, removed)
			a.exitRulesSvc.StaggeredSync() // 2026-07-07: issue #12 — staggered
		}
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("autoupdater: stopping")
			return
		case <-t.C:
			if !checkEnabled() {
				continue // toggle flipped off — skip this tick
			}
			added, removed, err := a.exitRulesSvc.DomainAutoUpdater()
			if err != nil {
				log.Printf("autoupdater: %v", err)
				continue
			}
			if added > 0 || removed > 0 {
				log.Printf("autoupdater: added=%d removed=%d, syncing exit-nodes", added, removed)
				a.exitRulesSvc.StaggeredSync() // 2026-07-07: issue #12
			}
		}
	}
}

// AdminTelegram is a thin wrapper preserved for the existing
// test surface (handlers_my_telegram_test.go calls
// app.AdminTelegram directly). Production routes go through
// adminSvc via the registration in cmd/skygate/main.go.
func (a *App) AdminTelegram(w http.ResponseWriter, r *http.Request) {
	if a.adminSvc != nil {
		a.adminSvc.AdminTelegram(w, r)
		return
	}
	http.Error(w, "admin service not wired", http.StatusInternalServerError)
}

// AdminTelegramPost is the POST counterpart of AdminTelegram.
func (a *App) AdminTelegramPost(w http.ResponseWriter, r *http.Request) {
	if a.adminSvc != nil {
		a.adminSvc.AdminTelegramPost(w, r)
		return
	}
	http.Error(w, "admin service not wired", http.StatusInternalServerError)
}

func New(d *sql.DB, hs *headscale.Client, headscaleKey, secret, controlURL, sshKeyPath string, sessionH int, cfg *config.Config) *App {
	derpURL := "http://derp.example.com:8766"
	if cfg != nil && cfg.DerpBaseURL != "" {
		derpURL = cfg.DerpBaseURL
	}
	a := &App{
		DB:           d,
		hs:           hs,
		HS:           hs,
		HeadscaleKey: headscaleKey,
		JWTSecret:    secret,
		ControlURL:   controlURL,
		SessionHours: sessionH,
		DerpBaseURL:  derpURL,
		// 2026-08-09 v0.33.1.31 B83: sshKeyPath is the
		// in-container path of the SSH private key used by
		// the per-row `exit_servers.ssh_key_path` fallback
		// (e.g. /admin/telegram egress handler reads
		// `s.SSHKeyPath` directly, NOT `s.Cfg.SSHKeyPath`,
		// so without this assignment the field stayed
		// zero-valued and the per-row `ssh_key_path` empty
		// case triggered "no ssh_key_path provided" from
		// headscale.SetAdvertisedRoutes). The Cfg field
		// ALSO holds the same value (it's the config-layer
		// copy); the /admin/exit-nodes/sync path uses
		// Cfg.SSHKeyPath, but the /admin/telegram path uses
		// App.SSHKeyPath, so both need to be populated.
		// v0.33.1 (the original sshKeyPath refactor) added
		// the field to the App struct and the parameter to
		// New() but missed the assignment here — pre-B83
		// the field was always empty. Live-verified via
		// /admin/telegram "Set as egress relay" against
		// emilia on the live VM (the 2026-08-09 operator
		// report that triggered the fix).
		SSHKeyPath:   sshKeyPath,
		templates:    LoadTemplates(),
		Notifier:    telegram.NoopNotifier{},
		I18n:         i18n.New(),
		Cfg:          cfg,
		// v0.26.0 — process-wide liveness/readiness fields.
		// InstanceID comes from SKYGATE_INSTANCE_ID env
		// (so multi-VM operators can tell which instance
		// answered a probe). BuildVersion is set later
		// by main.go (after the build hash is known).
		// StartedAt is set below.
		InstanceID: getenvOr("SKYGATE_INSTANCE_ID", "unconfigured"),
		StartedAt:  time.Now().UTC(),
		// refactor-v0.30 Phase D3 (2026-07-29): the
		// per-user control plane routing is now a
		// separate *controlplane.Router owned by the
		// App. The old hsCache + hsCacheMu fields +
		// the *App.HSForUser / HSGlobal / PlaneURLForUser
		// / InvalidateHSCache / InitHSForUserState
		// methods have all moved to that type. The
		// App keeps thin delegate methods (see
		// app_controlplane.go) so existing callers
		// (handlers_export.go, app_controlplane_test.go)
		// keep working.
		Router: controlplane.New(d, secret, hs),
	}
	a.Router.Init()
	return a
}

// getenvOr is a tiny helper for New()'s inline env
// lookups. Not exported — the production env-reader is
// config.Load, but the New() function is the wrong place
// to depend on a fully-loaded config (it would create a
// circular init at boot). The env var we want is so
// simple (a single identifier) that a one-liner is fine.
func getenvOr(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

// render executes a template directly (no layout). Used for self-contained pages.
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	// 2026-07-11: publish per-request lang to the funcmap before ExecuteTemplate.
	// The funcmap `t` / `tf` helpers read i18n.GlobalLang atomically.
	lang := a.I18n.LangFromRequest(r)
	i18n.SetLang(lang)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), 500)
	}
}

// renderWithLayout wraps a fragment template in the layout. data is merged into
// the wrapper, so handlers can add per-page fields (Nodes, Users, Entries, ...).
// IsAdmin and Page are auto-derived from c (the JWT claims) so admin nav stays visible.
func (a *App) renderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	// 2026-07-11: i18n. Detect lang and publish it to the funcmap. The funcmap
	// helpers `t` / `tf` in templates.go read i18n.GlobalLang atomically, so
	// concurrent requests each see their own language without a data race.
	lang := a.I18n.LangFromRequest(r)
	i18n.SetLang(lang)
	data["Lang"] = lang
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

	// 2026-07-20: v0.18.1 — auto-inject ControlURL so every
	// page template can reference {{.ControlURL}} without
	// the handler having to remember to pass it. Previously
	// `user/preauth_result.html` and `admin/exit_nodes.html`
	// referenced {{.ControlURL}} but the handlers didn't
	// pass it, so the rendered HTML showed an empty
	// `--login-server=`. The fix: renderWithLayout always
	// populates ControlURL from a.ControlURL (which the
	// caller set in New from cfg.ControlURL — the human-
	// facing URL clients should connect to, e.g.
	// https://head.example.com). Handlers can still override
	// it by passing their own "ControlURL" in the data map
	// (the for-loop below preserves caller values).
	data["ControlURL"] = a.ControlURL

	// 2026-08-09: v0.33.1.24 (B73) — auto-inject GitHub
	// org/repo so the layout's "Open release" fallback
	// button can build a `github.com/<owner>/<repo>/releases`
	// URL without hardcoding the operator's GitHub org. The
	// pre-fix layout had a literal
	// `https://github.com/skygate-operator/skygate/releases`
	// fallback that leaked the original developer's org name
	// (the v0.32.29 no-personal-data policy violation; flagged
	// in the v0.33.1.23 release notes). Defaults to
	// "BarsSky" / "skygate" if Cfg is nil (test paths).
	githubOwner := "BarsSky"
	githubRepo := "skygate"
	if a.Cfg != nil {
		if a.Cfg.GitHubOwner != "" {
			githubOwner = a.Cfg.GitHubOwner
		}
		if a.Cfg.GitHubRepo != "" {
			githubRepo = a.Cfg.GitHubRepo
		}
	}
	data["GitHubOwner"] = githubOwner
	data["GitHubRepo"] = githubRepo

	// 2026-07-15: v0.14.0 — release-monitor banner. We
	// only surface the banner to admins (regular users
	// don't need upgrade prompts). The data shape is
	// pre-computed here (rather than inside the template)
	// so the conditional is one line in the layout.
	if a.ReleaseMonitor != nil {
		latest, hasUpdate, checkedAt := a.ReleaseMonitor.Snapshot()
		if hasUpdate {
			data["UpdateAvailable"] = true
			// 2026-08-09: v0.33.1.23 (B72) — split the
			// release struct into two string fields so the
			// layout template can read them without runtime
			// type-dispatch. Before this fix, /admin/update
			// (which sets `UpdateLatest` to result.Latest, a
			// string) and the auto-injected banner (which
			// used to set it to the whole `Release` struct)
			// had inconsistent shapes; the template assumed
			// the struct shape (`{{.UpdateLatest.TagName}}`)
			// and crashed at execute time when /admin/update
			// set it as a string. Pin: UpdateLatest is
			// always a tag name string; UpdateLatestURL is
			// always a release-page URL string.
			data["UpdateLatest"] = latest.TagName
			data["UpdateLatestURL"] = latest.HTMLURL
			data["UpdateCheckedAt"] = checkedAt
		}
	}
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
// per-request language so the title follows the chosen language.
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
			// 2026-07-11: Этап 10 part 2 — SQL moved to db helpers.
			// We still need to walk every row because the stored
			// token_hash is a bcrypt hash (see auth.GenerateAPIToken),
			// so we have to CompareHashAndPassword every candidate
			// — there's no way to do an indexed lookup.
			// 2026-07-16: v0.15.5 — filter out expired tokens
			// (TTL = 0 means "never expires" — the pre-v0.15.5
			// behaviour, preserved for legacy rows).
			candidates, err := db.ListAPITokenHashesForLookup(a.DB)
			if err == nil {
				now := time.Now().Unix()
				for _, c := range candidates {
					if c.ExpiresAt > 0 && c.ExpiresAt <= now {
						// Token has expired. Skip — keep walking
						// the candidates in case a sibling row
						// has a matching hash (extremely rare —
						// token_hash is unique, but be defensive).
						continue
					}
					if auth.CheckAPIToken(c.TokenHash, tok) {
						_ = db.TouchAPITokenLastUsed(a.DB, c.TokenHash)
						return &auth.Claims{UserID: c.UserID, Username: c.Username, IsAdmin: c.IsAdmin}
					}
				}
			}
		}
	}
	return nil
}

// audit writes a row to the audit log.
func (a *App) audit(userID int64, username, action, detail string) {
	// 2026-07-11: Этап 9 part 2 — INSERT moved to db.AppendAuditLog
	// so the SQL string lives in queries.go. Audit failures remain
	// best-effort (the error is intentionally dropped) — a transient
	// DB hiccup must not break the main action (login, rule add, etc).
	_ = db.AppendAuditLog(a.DB, userID, username, action, detail)
}

// SanitizeFilename is a thin re-export of httputil.SanitizeFilename
// kept for backward compatibility — it was the original home of
// the helper (Phase B step 3b.5, 2026-07-29) and several
// non-feature-package files still import handlers.SanitizeFilename.
// The single source of truth now lives in internal/httputil/.
//
// refactor-v0.30 Phase D1 (2026-07-29): the implementation
// moved to internal/httputil/sanitize.go (eliminating 3
// near-identical copies in handlers.go + feature/admin +
// feature/my). This function delegates to it.
func SanitizeFilename(s string) string {
	return httputil.SanitizeFilename(s)
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
//   - handlers_admin_users.go       — /admin/users
//   - handlers_admin_nodes.go       — /admin/devices (tag/untag)
//   - handlers_admin_pages.go       — /admin/audit, /admin/acls
//   - handlers_derp.go              — /admin/derp + DERP types
//
// Node-ownership backfill now lives in internal/nodeownership/
// (Phase D2, 2026-07-29). The thin App wrapper
// `BackfillNodeOwnershipFn` in handlers_export.go is the
// only remaining bridge — feature/my uses it via the
// Service.BackfillNodeOwnership callback.
//
// See AGENTS.md "Sister files" for current line counts.
