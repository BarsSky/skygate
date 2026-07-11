package handlers

// handlers_help.go — /help page handler.
// Extracted from handlers.go.

import (
	"net/http"
)

// GetHelp renders the in-portal help page. Visible to all
// authenticated users. The actual content lives in
// templates/help.html.
func (a *App) GetHelp(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, r, "help.html", c, map[string]any{})
}
