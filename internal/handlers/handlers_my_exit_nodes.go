package handlers

// handlers_my_exit_nodes.go — GET /my/exit-nodes: list exit nodes the
// user can route through. Visible to all authenticated users.
// Extracted from handlers.go.

import (
	"net/http"
)

// GetExitNodes lists exit nodes advertised in the tailnet. Visible to all
// authenticated users so they can pick one to route through.
func (a *App) GetExitNodes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// 2026-07-15: v0.12.0 — route to the user's own control plane.
	// Exit nodes belong to the user's tailnet, so the list
	// reflects their headscale instance, not the operator's
	// primary one. (A user on headscale-B sees headscale-B's
	// exit nodes, not headscale-A's.)
	exits, _ := a.HSForUser(c.UserID).ListExitNodes()
	a.renderWithLayout(w, r, "user/exit_nodes.html", c, map[string]any{
		"ExitNodes": exits,
	})
}
