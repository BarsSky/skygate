package my

// settings.go — user-facing per-user settings (theme switcher).
// Moved from internal/handlers/handlers_settings.go (63 lines).
//
// refactor-v0.30 Phase B step 6e (2026-07-29). Lives in
// feature/my because the theme is a per-user preference and
// the /settings/theme endpoint is reachable by any user
// (logged-in or not — the handler redirects to /login?theme=
// for the unauth case so the chosen theme survives the
// post-login redirect).

import (
	"net/http"
	"strings"

	"skygate/internal/db"
)

// PostSettingsTheme updates the user's theme preference and bounces back.
// Accepts both POST (form submission) and GET (theme preview from
// the picker — the legacy /settings/theme endpoint handles both).
func (s *Service) PostSettingsTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if !db.IsValidTheme(theme) {
		theme = r.URL.Query().Get("theme")
	}
	if !db.IsValidTheme(theme) {
		http.Error(w, "invalid theme", http.StatusBadRequest)
		return
	}
	c := s.Backend.CurrentUser(r)
	if c == nil {
		// not logged in - just bounce to login with theme in URL
		http.Redirect(w, r, "/login?theme="+theme, http.StatusFound)
		return
	}
	if err := db.SetUserTheme(s.DB, c.UserID, theme); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "theme_change", theme)
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
