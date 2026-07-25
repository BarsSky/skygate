package handlers

// handlers_my_exit_nodes.go — GET /my/exit-nodes: list exit nodes the
// user can route through. Visible to all authenticated users.
// Extracted from handlers.go.

import (
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
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
	// 2026-07-24: v0.28.1 — also pass the user's current
	// preferred exit-node tag so the template can render
	// "Set as my preferred" / "Currently preferred" buttons
	// per exit-node row. Empty string = no preference set.
	// 2026-07-25: v0.28.5 — also pass ViaEnabled for the
	// "Strict pinning" checkbox state.
	var prefTag string
	var viaEnabled bool
	if pref, err := db.GetUserExitNodePref(a.DB, c.UserID); err == nil {
		prefTag = pref.ExitNodeTag
		viaEnabled = pref.ViaEnabled
	}
	a.renderWithLayout(w, r, "user/exit_nodes.html", c, map[string]any{
		"ExitNodes":           exits,
		"PreferredExitNodeTag": prefTag,
		"ViaEnabled":          viaEnabled,
		"FlashSuccess":        r.URL.Query().Get("ok"),
		"FlashError":          r.URL.Query().Get("err"),
	})
}

// PostMyExitNodePreferred sets (or clears) the caller's
// preferred exit-node. Form field `tag` carries the
// derived headscale tag (e.g. "tag:exit-relay-1"); an
// empty value clears the preference. After the DB
// write, an ACL re-apply pushes the new `via` to
// headscale so the next /my/devices load sees the
// effective policy.
//
// 2026-07-24: v0.28.1. Visible to all authenticated
// users (self-service). Admin path is
// /admin/users/{id}/subnet/preferred-exit.
func (a *App) PostMyExitNodePreferred(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	tag := strings.TrimSpace(r.FormValue("tag"))
	// 2026-07-25: v0.28.5 — strict pinning is opt-in.
	// The form posts a `via` field ("1" to enable the
	// headscale packet-filter pinning, anything else
	// for the safe default). Older Tailscale clients
	// (notably Android) reject policies with via they
	// don't understand, so the default is OFF.
	viaEnabled := r.FormValue("via") == "1"
	if err := db.SetUserExitNodePref(a.DB, c.UserID, tag, c.UserID, viaEnabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "my_preferred_exit_set",
		"tag="+tag+" via="+strconv.FormatBool(viaEnabled))
	// Re-apply ACL. The user's planeURL is "" for the
	// global-default single-plane deploy (the only path
	// the bot / web form support today). v0.12.0
	// per-plane dispatch is on the call site — we use
	// HSForUser(c.UserID) so the push lands on the
	// user's own headscale.
	res := acl.ApplyACLPipelineForPlane(a.DB, a.HSForUser(c.UserID), "", nil, c.Username,
		"my_preferred_exit_set tag="+tag, a.Cfg.ACLWithViaEnabled)
	if !res.Applied {
		// The preference is in the DB regardless — the
		// next /my/devices load will retry the policy
		// build. We redirect with a flash so the user
		// sees the error.
		http.Redirect(w, r, "/my/exit-nodes?err="+errToQuery(res.Err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/my/exit-nodes?ok=1", http.StatusSeeOther)
}

func errToQuery(s string) string {
	// URL-encode just enough to keep query params valid.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			out = append(out, '+')
		case c == '\n':
			out = append(out, '%', '0', 'A')
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~':
			out = append(out, c)
		default:
			// %XX (uppercase hex).
			out = append(out, '%')
			out = append(out, "0123456789ABCDEF"[c>>4])
			out = append(out, "0123456789ABCDEF"[c&0xF])
		}
	}
	return string(out)
}
