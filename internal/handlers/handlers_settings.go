package handlers

// handlers_settings.go — user-facing settings (theme switcher).
// Extracted from handlers.go.

import (
	"net/http"
	"strings"

	"skygate/internal/db"
)

// PostSettingsTheme updates the user's theme preference and bounces back.
func (a *App) PostSettingsTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if !db.IsValidTheme(theme) {
		theme = r.URL.Query().Get("theme")
	}
	if !db.IsValidTheme(theme) {
		http.Error(w, "invalid theme", 400)
		return
	}
	c := a.currentUser(r)
	if c == nil {
		// not logged in - just bounce to login with theme in URL
		http.Redirect(w, r, "/login?theme="+theme, http.StatusFound)
		return
	}
	if err := db.SetUserTheme(a.DB, c.UserID, theme); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "theme_change", theme)
	// back to wherever the user came from
	ref := r.Referer()
	if ref == "" {
		ref = "/dashboard"
	}
	// strip old theme query so we don't loop
	if strings.Contains(ref, "theme=") {
		ref = stripThemeParam(ref)
	}
	http.Redirect(w, r, ref, http.StatusFound)
}

func stripThemeParam(url string) string {
	if i := strings.Index(url, "?"); i >= 0 {
		qs := url[i+1:]
		parts := strings.Split(qs, "&")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if !strings.HasPrefix(p, "theme=") {
				out = append(out, p)
			}
		}
		prefix := url[:i]
		if len(out) == 0 {
			return prefix
		}
		return prefix + "?" + strings.Join(out, "&")
	}
	return url
}
