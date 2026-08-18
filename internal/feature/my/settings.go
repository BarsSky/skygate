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
	"fmt"
	"net/http"
	"strconv"
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

// PostMyAccountDisplay updates the per-user display preferences
// (font_family, font_scale, selection_bg) and redirects back to
// /my/account. B136 (v1.3.20.6) — these are stored in
// portal_users (NOT in localStorage) so the operator's display
// prefs follow them across devices and survive cache clears.
//
// form fields:
//   - font_family  : "manrope" | "inter" | "geist" | "sora" | "system"
//                    (validated; unknown → "manrope")
//   - font_scale   : -2..+2 integer (clamped; out-of-range → 0)
//   - selection_bg : CSS color string ("#rrggbb" or "rgba(...)" or
//                    "transparent" or empty for theme default)
//
// All 3 fields are optional in the form — missing fields keep
// the user's current value. This is intentional so the user
// can change ONE setting (e.g. font size) without re-typing
// the others. The handler reads the existing prefs and only
// overwrites the fields the user actually submitted.
func (s *Service) PostMyAccountDisplay(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Start from the existing prefs (so omitted fields keep their value).
	cur := db.GetUserDisplayPrefs(s.DB, c.UserID)
	if family := r.FormValue("font_family"); family != "" {
		cur.FontFamily = family
	}
	if scale := r.FormValue("font_scale"); scale != "" {
		n, err := strconv.Atoi(scale)
		if err == nil {
			cur.FontScale = n
		}
	}
	if sel := r.FormValue("selection_bg"); sel != "" {
		cur.SelectionBg = strings.TrimSpace(sel)
	}
	if err := db.SetUserDisplayPrefs(s.DB, c.UserID, cur); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "display_prefs_error", err.Error())
		http.Redirect(w, r, "/my/account?err=display_save_failed", http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "display_prefs_change",
		fmt.Sprintf("%s|%d|%s", cur.FontFamily, cur.FontScale, cur.SelectionBg))
	http.Redirect(w, r, "/my/account?saved=display", http.StatusFound)
}
