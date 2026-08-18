package auth

// service.go — the auth feature module. Owns the /login, /logout,
// /lang, /my/account, /my/account/password, /my/tokens, /my/token,
// and /my/token/{id}/revoke routes.
//
// refactor-v0.30 Phase B step 2 (2026-07-29): handlers moved from
// internal/handlers/{handlers_auth,handlers_my_account,handlers_api_tokens}.go
// to internal/feature/auth/. The Service takes its dependencies as
// plain fields + a Backend interface, decoupling this package from
// the legacy *App struct.
//
// Why a Backend interface (and not just *App):
//   The Service needs to call a handful of cross-cutting helpers
//   that the legacy handlers used as methods on *App:
//   - Render / RenderWithLayout (template execution)
//   - CurrentUser (JWT cookie → claims)
//   - Audit (audit log writer)
//   The interface lets the Service depend on just those four
//   methods, not the whole App. The capital-letter wrappers
//   added in internal/handlers/handlers_export.go (Render,
//   RenderWithLayout, CurrentUser, Audit) satisfy the interface;
//   any other backend (e.g. a future test double) can too.

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
)

// Backend is the minimum surface the auth feature needs from the
// host application. *App satisfies it via the capital-letter
// wrappers in internal/handlers/handlers_export.go.
type Backend interface {
	Render(w http.ResponseWriter, r *http.Request, name string, data any)
	RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any)
	CurrentUser(r *http.Request) *auth.Claims
	Audit(userID int64, username, action, detail string)
}

// Service is the auth feature module. One Service is created at
// boot by cmd/skygate/main.go and registered as the handler for
// all auth-related routes. All fields are read-only after
// construction.
//
// Field semantics:
//   - Backend:     satisfies the Backend interface (typically *App).
//   - DB:          the open *sql.DB. Used for user lookups, token
//                  storage, and password-hash writes.
//   - I18n:        the i18n catalog (lang from cookie / Accept-Language).
//   - JWTSecret:   the HMAC secret for new session JWTs.
//   - SessionHours: default session lifetime in hours. "Remember me"
//                  extends this to 30 days (720h) on the login form.
//   - Version:     the build version string, shown on the login page.
type Service struct {
	Backend      Backend
	DB           *sql.DB
	I18n         *i18n.Catalog
	JWTSecret    string
	SessionHours int
	Version      string
}

// ---------- AUTH: /login, /logout, /lang ----------

// GetLogin renders the login form. Resolves the active theme from
// (in priority order) the ?theme= query, the cookie from a previous
// session, and finally the system default.
func (s *Service) GetLogin(w http.ResponseWriter, r *http.Request) {
	theme := db.ThemeLinear
	if t := r.URL.Query().Get("theme"); db.IsValidTheme(t) {
		theme = t
	} else if c, _ := r.Cookie("skygate_session"); c != nil {
		if claims, err := auth.ParseJWT(s.JWTSecret, c.Value); err == nil {
			theme = db.GetUserTheme(s.DB, claims.UserID)
		}
	}
	lang := s.I18n.LangFromRequest(r)
	data := map[string]any{
		"Error":      "",
		"Theme":      theme,
		"ThemeLabel": db.ThemeLabel(theme),
		"Lang":       lang,
		"Version":    s.Version,
	}
	// 2026-07-17: v0.16.8 — pre-fill username from "last_username" cookie.
	if c, err := r.Cookie("last_username"); err == nil && c.Value != "" {
		data["LastUsername"] = c.Value
	}
	s.Backend.Render(w, r, "login.html", data)
}

// PostLogin validates username + password, issues a JWT cookie,
// and redirects to /dashboard. On "Remember me", extends the
// session cookie from SessionHours to 30 days.
func (s *Service) PostLogin(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.FormValue("username"))
	p := r.FormValue("password")
	remember := r.FormValue("remember") == "1"
	lang := s.I18n.LangFromRequest(r)
	baseData := map[string]any{
		"Theme":      db.ThemeLinear,
		"ThemeLabel": db.ThemeLabel(db.ThemeLinear),
		"Lang":       lang,
		"Version":    s.Version,
	}
	// 2026-07-17: v0.16.8 — pre-fill username from "last_username" cookie.
	if c, err := r.Cookie("last_username"); err == nil && c.Value != "" {
		baseData["LastUsername"] = c.Value
	}
	if u == "" || p == "" {
		baseData["Error"] = s.I18n.T(lang, "login.invalid_credentials")
		s.Backend.Render(w, r, "login.html", baseData)
		return
	}
	id, hash, isAdmin, err := db.GetUserCredentials(s.DB, u)
	if err != nil || !auth.CheckPassword(hash, p) {
		s.Backend.Audit(id, u, "login_fail", "")
		baseData["Error"] = s.I18n.T(lang, "login.invalid_credentials")
		s.Backend.Render(w, r, "login.html", baseData)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 2026-07-17: v0.16.8 — "Remember me" extends the session cookie.
	sessionHours := s.SessionHours
	if remember {
		sessionHours = 30 * 24
	}
	tok, err := auth.IssueJWT(s.JWTSecret, id, u, isAdmin, sessionHours)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(id, u, "login_ok", "")
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   sessionHours * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	// 2026-07-17: v0.16.8 — remember the username in a long-lived cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     "last_username",
		Value:    u,
		Path:     "/",
		HttpOnly: false,
		MaxAge:   365 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// PostLang sets the lang cookie from a POST form, then redirects
// back to return_to (or /dashboard).
func (s *Service) PostLang(w http.ResponseWriter, r *http.Request) {
	lang := strings.ToLower(strings.TrimSpace(r.FormValue("lang")))
	if lang != i18n.LangEN && lang != i18n.LangRU {
		lang = i18n.LangRU
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "lang",
		Value:    lang,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	returnTo := r.FormValue("return_to")
	if returnTo == "" {
		returnTo = "/dashboard"
	}
	if !strings.HasPrefix(returnTo, "/") {
		returnTo = "/dashboard"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// PostLogout clears the session cookie and redirects to /login.
func (s *Service) PostLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "skygate_session", Value: "", Path: "/", MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---------- /my/account (self-service password change) ----------

// GetMyAccount renders the account page with a password-change form.
func (s *Service) GetMyAccount(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// B136 (v1.3.20.6): load per-user display prefs (font + size + selection
	// color) so the /my/account form can show the current values and the
	// layout can inject the right <style> block.
	prefs := db.GetUserDisplayPrefs(s.DB, c.UserID)
	s.Backend.RenderWithLayout(w, r, "user/account.html", c, map[string]any{
		"Page":             "account",
		"Title":            "Account",
		"FlashOK":          r.URL.Query().Get("saved"),
		"FlashError":       r.URL.Query().Get("err"),
		"DisplayFont":      prefs.FontFamily,
		"DisplayScale":     prefs.FontScale,
		"DisplaySelBg":     prefs.SelectionBg,
		"DisplayFontLabel": db.FontFamilyLabel(prefs.FontFamily),
	})
}

// PostMyAccountPassword validates current + new + confirm and writes
// the new password hash. On success redirects to /my/account?saved=ok;
// on failure sets ?err=... for the template to surface.
func (s *Service) PostMyAccountPassword(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/account?err=bad_form", http.StatusFound)
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_new_password")

	if current == "" || next == "" || confirm == "" {
		http.Redirect(w, r, "/my/account?err=fields_empty", http.StatusFound)
		return
	}
	if next != confirm {
		http.Redirect(w, r, "/my/account?err=passwords_dont_match", http.StatusFound)
		return
	}
	if len(next) < 8 {
		http.Redirect(w, r, "/my/account?err=password_too_short", http.StatusFound)
		return
	}

	hash, err := db.GetPasswordHashByID(s.DB, c.UserID)
	if err != nil {
		http.Redirect(w, r, "/my/account?err=user_not_found", http.StatusFound)
		return
	}
	if !auth.CheckPassword(hash, current) {
		s.Backend.Audit(c.UserID, c.Username, "password_change_fail", "wrong current")
		http.Redirect(w, r, "/my/account?err=wrong_current_password", http.StatusFound)
		return
	}

	newHash, err := auth.HashPassword(next)
	if err != nil {
		http.Redirect(w, r, "/my/account?err=hash_failed", http.StatusFound)
		return
	}
	if _, err := db.UpdatePasswordHash(s.DB, c.UserID, newHash); err != nil {
		http.Redirect(w, r, "/my/account?err=db_error", http.StatusFound)
		return
	}

	s.Backend.Audit(c.UserID, c.Username, "password_change", "")
	http.Redirect(w, r, "/my/account?saved=ok", http.StatusFound)
}

// ---------- /my/tokens (personal API tokens for AI integration) ----------

// GetMyTokens lists the caller's API tokens with formatted timestamps
// (last-used, expires-at). Each token is shown only once at creation
// (see PostMyToken).
func (s *Service) GetMyTokens(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	tokens, err := db.ListAPITokensByUser(s.DB, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tokens == nil {
		tokens = []db.APIToken{}
	}
	rows := make([]map[string]string, 0, len(tokens))
	for _, t := range tokens {
		last := "—"
		if !t.LastUsed.IsZero() {
			last = t.LastUsed.Format("2006-01-02 15:04")
		}
		exp := "—"
		if !t.ExpiresAt.IsZero() {
			exp = t.ExpiresAt.Format("2006-01-02 15:04")
		}
		rows = append(rows, map[string]string{
			"ID":         fmt.Sprintf("%d", t.ID),
			"Label":      t.Label,
			"LastUsed":   last,
			"Created":    t.CreatedAt.Format("2006-01-02 15:04"),
			"Expires":    exp,
			"AutoRotate": fmt.Sprintf("%v", t.AutoRotate),
		})
	}
	s.Backend.RenderWithLayout(w, r, "my_tokens.html", c, map[string]any{
		"Page":  "tokens",
		"Title": "API Tokens",
		"Tokens": rows,
	})
}

// PostMyToken creates a new API token. The raw token is shown ONCE
// in the response — only the hash is stored (db.InsertAPIToken).
// TTL dropdown: "1h" / "1d" / "7d" / "30d" / "never" (v0.15.5).
func (s *Service) PostMyToken(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	label := r.FormValue("label")
	// 2026-07-16: v0.15.5 — TTL dropdown. "1h" / "1d" /
	// "7d" / "30d" / "never". Any other value falls through
	// to "never" (the v0.15.0 default) so an old / buggy
	// form can't lock the user out.
	expiresAt := int64(0)
	switch r.FormValue("ttl") {
	case "1h":
		expiresAt = time.Now().Add(time.Hour).Unix()
	case "1d":
		expiresAt = time.Now().Add(24 * time.Hour).Unix()
	case "7d":
		expiresAt = time.Now().Add(7 * 24 * time.Hour).Unix()
	case "30d":
		expiresAt = time.Now().Add(30 * 24 * time.Hour).Unix()
	case "never", "":
		expiresAt = 0
	}
	autoRotate := r.FormValue("auto_rotate") == "1"
	raw, hash := auth.GenerateAPIToken()
	if _, err := db.InsertAPIToken(s.DB, c.UserID, hash, label, expiresAt, autoRotate); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail := fmt.Sprintf("label=%q ttl=%s auto_rotate=%v", label, r.FormValue("ttl"), autoRotate)
	s.Backend.Audit(c.UserID, c.Username, "token_create", detail)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<div class=\"card\"><h3>Токен создан</h3><p>Скопируйте сейчас — больше он показан не будет.</p><pre style=\"background:var(--bg);padding:12px;border-radius:4px;word-break:break-all\">%s</pre><p><a href=\"/my/tokens\">← Назад к списку</a></p></div>", raw)
}

// PostMyTokenRevoke deletes one of the caller's API tokens. Bad id
// is a no-op (matches the pre-refactor behavior).
func (s *Service) PostMyTokenRevoke(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	_, _ = db.DeleteAPITokenByUser(s.DB, id, c.UserID)
	s.Backend.Audit(c.UserID, c.Username, "token_revoke", idStr)
	http.Redirect(w, r, "/my/tokens?revoked=1", http.StatusFound)
}
