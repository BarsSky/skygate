package handlers

import (
	"net/http"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
)

// ---------- AUTH ----------

func (a *App) GetLogin(w http.ResponseWriter, r *http.Request) {
	// Resolve theme: ?theme=... on the URL wins; otherwise last user theme if any.
	theme := db.ThemeLinear
	if t := r.URL.Query().Get("theme"); db.IsValidTheme(t) {
		theme = t
	} else if c, _ := r.Cookie("skygate_session"); c != nil {
		if claims, err := auth.ParseJWT(a.JWTSecret, c.Value); err == nil {
			theme = db.GetUserTheme(a.DB, claims.UserID)
		}
	}
	lang := a.I18n.LangFromRequest(r)
	data := map[string]any{
		"Error":      "",
		"Theme":      theme,
		"ThemeLabel": db.ThemeLabel(theme),
		"Lang":       lang,
		"Version":    a.Version,
	}
	// 2026-07-17: v0.16.8 — pre-fill username from "last_username" cookie.
	if c, err := r.Cookie("last_username"); err == nil && c.Value != "" {
		data["LastUsername"] = c.Value
	}
	a.render(w, r, "login.html", data)
}

func (a *App) PostLogin(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.FormValue("username"))
	p := r.FormValue("password")
	remember := r.FormValue("remember") == "1"
	lang := a.I18n.LangFromRequest(r)
	baseData := map[string]any{
		"Theme":      db.ThemeLinear,
		"ThemeLabel": db.ThemeLabel(db.ThemeLinear),
		"Lang":       lang,
		"Version":    a.Version,
	}
	// 2026-07-17: v0.16.8 — pre-fill username from "last_username" cookie so the
	// user doesn't have to retype it on every logout/login cycle. Cookie is
	// not HttpOnly because the template needs to read it server-side; it does
	// not contain any credential material (only the username string).
	if c, err := r.Cookie("last_username"); err == nil && c.Value != "" {
		baseData["LastUsername"] = c.Value
	}
	if u == "" || p == "" {
		baseData["Error"] = a.I18n.T(lang, "login.invalid_credentials")
		a.render(w, r, "login.html", baseData)
		return
	}
	var id int64
	var hash string
	var isAdmin bool
	id, hash, isAdmin, err := db.GetUserCredentials(a.DB, u)
	if err != nil || !auth.CheckPassword(hash, p) {
		a.audit(id, u, "login_fail", "")
		baseData["Error"] = a.I18n.T(lang, "login.invalid_credentials")
		a.render(w, r, "login.html", baseData)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// 2026-07-17: v0.16.8 — "Remember me" extends the session cookie from
	// SessionHours (24h default) to 30 days. Browser-side autofill is
	// independent (login.html uses autocomplete="username" / "current-password"
	// and the password manager saves the credentials).
	sessionHours := a.SessionHours
	if remember {
		sessionHours = 30 * 24
	}
	tok, err := auth.IssueJWT(a.JWTSecret, id, u, isAdmin, sessionHours)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(id, u, "login_ok", "")
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   sessionHours * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	// 2026-07-17: v0.16.8 — remember the username in a long-lived cookie
	// so the next /login visit pre-fills it. Same lifetime as lang cookie
	// (365 days). Not HttpOnly (template needs the value); the cookie
	// holds only the username, no credential material.
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

// 2026-07-10: i18n. Set the lang cookie from a POST form, then redirect back.
func (a *App) PostLang(w http.ResponseWriter, r *http.Request) {
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

func (a *App) PostLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "skygate_session", Value: "", Path: "/", MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}
