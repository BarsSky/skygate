// Package my — exit_nodes.go owns the /my/exit-nodes
// self-service page (list exit nodes the user can route
// through + set/clear preferred exit-node).
//
// refactor-v0.30 Phase B step 5a (2026-07-29): moved from
// internal/handlers/handlers_my_exit_nodes.go. The two
// handlers (GetExitNodes + PostMyExitNodePreferred) + the
// errToQuery helper used to be methods on *App; they now
// live on *Service.
package my

import (
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// GetExitNodes lists exit nodes advertised in the tailnet.
// Visible to all authenticated users so they can pick
// one to route through.
//
// 2026-07-15: v0.12.0 — route to the user's own control
// plane. Exit nodes belong to the user's tailnet, so the
// list reflects their headscale instance, not the
// operator's primary one. (A user on headscale-B sees
// headscale-B's exit nodes, not headscale-A's.)
//
// 2026-07-24: v0.28.1 — also pass the user's current
// preferred exit-node tag so the template can render
// "Set as my preferred" / "Currently preferred" buttons
// per exit-node row.
//
// 2026-07-25: v0.28.5 — also pass ViaEnabled for the
// "Strict pinning" checkbox state.
func (s *Service) GetExitNodes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	exits, _ := s.Backend.HSForUserFn(c.UserID).ListExitNodes()
	var prefTag string
	var viaEnabled bool
	if pref, err := db.GetUserExitNodePref(s.DB, c.UserID); err == nil {
		prefTag = pref.ExitNodeTag
		viaEnabled = pref.ViaEnabled
	}
	s.Backend.RenderWithLayout(w, r, "user/exit_nodes.html", c, map[string]any{
		"ExitNodes":            exits,
		"PreferredExitNodeTag": prefTag,
		"ViaEnabled":           viaEnabled,
		"FlashSuccess":         r.URL.Query().Get("ok"),
		"FlashError":           r.URL.Query().Get("err"),
	})
}

// PostMyExitNodePreferred sets (or clears) the caller's
// preferred exit-node. Form field `tag` carries the
// derived headscale tag (e.g. "tag:exit-emilia"); an
// empty value clears the preference. After the DB
// write, an ACL re-apply pushes the new `via` to
// headscale so the next /my/devices load sees the
// effective policy.
//
// 2026-07-24: v0.28.1. Visible to all authenticated
// users (self-service). Admin path is
// /admin/users/{id}/subnet/preferred-exit (lives in
// feature/admin — see admin/user_subnet.go).
func (s *Service) PostMyExitNodePreferred(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	if err := db.SetUserExitNodePref(s.DB, c.UserID, tag, c.UserID, viaEnabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "my_preferred_exit_set",
		"tag="+tag+" via="+strconv.FormatBool(viaEnabled))
	// Re-apply ACL. The user's planeURL is "" for the
	// global-default single-plane deploy (the only path
	// the bot / web form support today). v0.12.0
	// per-plane dispatch is on the call site — we use
	// HSForUser(c.UserID) so the push lands on the
	// user's own headscale.
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	res := acl.ApplyACLPipelineForPlane(s.DB, s.Backend.HSForUserFn(c.UserID), "", nil, c.Username,
		"my_preferred_exit_set tag="+tag, viaFlag)
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

// errToQuery URL-encodes a string for use in a redirect
// query parameter. Spaces become '+' (matching the
// application/x-www-form-urlencoded form), common
// safe-chars pass through, everything else becomes
// %XX (uppercase hex). Used by the preferred-exit
// handlers when redirecting with a flash error.
func errToQuery(s string) string {
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
