package handlers

import (
	"database/sql"
	"errors"
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
	a.render(w, "login.html", map[string]any{
		"Error":      "",
		"Theme":      theme,
		"ThemeLabel": db.ThemeLabel(theme),
		"Lang":       lang,
		"T":          &i18n.Translations{Catalog: a.I18n, Lang: lang},
	})
}

func (a *App) PostLogin(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.FormValue("username"))
	p := r.FormValue("password")
	lang := a.I18n.LangFromRequest(r)
	baseData := map[string]any{
		"Theme":      db.ThemeLinear,
		"ThemeLabel": db.ThemeLabel(db.ThemeLinear),
		"Lang":       lang,
		"T":          &i18n.Translations{Catalog: a.I18n, Lang: lang},
	}
	if u == "" || p == "" {
		baseData["Error"] = a.I18n.T(lang, "login.invalid_credentials")
		a.render(w, "login.html", baseData)
		return
	}
	var id int64
	var hash string
	var isAdmin int
	err := a.DB.QueryRow(`SELECT id, password_hash, is_admin FROM portal_users WHERE username=?`, u).
		Scan(&id, &hash, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) || !auth.CheckPassword(hash, p) {
		a.audit(id, u, "login_fail", "")
		baseData["Error"] = a.I18n.T(lang, "login.invalid_credentials")
		a.render(w, "login.html", baseData)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tok, err := auth.IssueJWT(a.JWTSecret, id, u, isAdmin == 1, a.SessionHours)
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
		MaxAge:   a.SessionHours * 3600,
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
