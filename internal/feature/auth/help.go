package auth

// help.go — /help page handler.
//
// refactor-v0.30 Phase B step 6e (2026-07-29): moved from
// internal/handlers/handlers_help.go (20 lines). The page
// is a static informational render — no DB access, no audit.
// Lives in feature/auth because the auth package already
// owns the small "user-facing informational" surface
// (/my/account, /my/tokens, /login, /logout) and adding a
// tiny one-route file here is cheaper than creating a new
// feature package.

import "net/http"

// GetHelp renders the in-portal help page. Visible to all
// authenticated users. The actual content lives in
// templates/help.html.
func (s *Service) GetHelp(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.Backend.RenderWithLayout(w, r, "help.html", c, map[string]any{})
}
