package handlers

// exit_rules_form_reapply.go — admin ACL re-apply.
//
// - PostAdminACLReapply (POST /admin/exit-rules/reapply)
//
// Regenerates the headscale policy from the current DB state and
// pushes it to headscale via SetPolicy. Use this when the policy
// shape changed (e.g. the GenerateACL() code now emits a new SSH
// rule) but no exit-rule add/delete has happened yet — the normal
// PostMyExitRule / PostDeleteExitRule paths are the only places
// that re-run SetPolicy, and a code-only change (no data change)
// won't trigger them on its own.
//
// 2026-07-16: v0.13.0 — per-plane ACL. The reapply now iterates
// every distinct control plane (one entry per distinct
// headscale_url, plus the global default) and pushes the
// right policy to each. Single-plane deploys see the same
// single SetPolicy call as before.
//
// Admin-only. Idempotent.

import (
	"fmt"
	"net/http"

	"skygate/internal/acl"
	"skygate/internal/headscale"
)

func (a *App) PostAdminACLReapply(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), 400)
		return
	}
	// v0.13.0 — per-plane iteration. The hsForPlane closure
	// resolves the headscale_url to a cached *headscale.Client
	// (same App.HSForUser / App.HSGlobal path the web and bot
	// use), so the reapply pushes the right policy to every
	// plane in one go. The alerter is the App's Notifier
	// (typed as acl.Alerter via the SendAlert signature).
	var alerter acl.Alerter
	if a.Notifier != nil {
		alerter = a.Notifier
	}
	results := acl.ApplyACLForAllPlanes(a.DB,
		func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return a.HSGlobal()
			}
			// Build a synthetic user-id=0 client lookup
			// for plane-only resolution. App.HSForUser
			// works on per-user overrides; for the
			// plane-URL-only case we just need the
			// (url, key) pair, which we can fetch via
			// db.GetUserHeadscaleConfig — but the API
			// takes a user id, not a url. For v0.13.0
			// minimal viable: any non-default plane's
			// first user has the right key (planes
			// share the same api_key today), so the
			// App's per-user cache does the job. If a
			// future release supports per-plane keys
			// (one key per plane URL, not per user),
			// this closure will need a separate
			// plane-key cache.
			rows, err := a.DB.Query("SELECT id FROM portal_users WHERE headscale_url = ? LIMIT 1", planeURL)
			if err != nil {
				return a.HSGlobal()
			}
			defer rows.Close()
			if !rows.Next() {
				return a.HSGlobal()
			}
			var uid int64
			if err := rows.Scan(&uid); err != nil {
				return a.HSGlobal()
			}
			return a.HSForUser(uid)
		},
		alerter,
		c.Username,
		fmt.Sprintf("reapply by %s (per-plane)", c.Username),
	)
	// v0.13.0 — single-plane deploys see one result, multi-
	// plane deploys see one per plane. Surface the first
	// failure (if any) and 200 on full success.
	for _, r := range results {
		if r.Err != nil {
			if a.Notifier != nil {
				go a.Notifier.SendAlert(fmt.Sprintf("❌ ACL reapply failed (by %s)\n  err: %v",
					c.Username, r.Err))
			}
			http.Error(w, "set policy: "+r.Err.Error(), 500)
			return
		}
	}
	if a.Notifier != nil {
		go a.Notifier.SendAlert(fmt.Sprintf("🔄 ACL reapply by %s → %d plane(s)", c.Username, len(results)))
	}
	http.Redirect(w, r, "/admin/exit-rules?reapplied=1", http.StatusSeeOther)
}
