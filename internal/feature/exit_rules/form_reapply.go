// Package exit_rules — form_reapply.go owns the admin
// ACL re-apply handler.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved
// from internal/handlers/exit_rules_form_reapply.go.
// Regenerates the headscale policy from the current DB
// state and pushes it to headscale via SetPolicy. Use
// this when the policy shape changed (e.g. the
// GenerateACL() code now emits a new SSH rule) but no
// exit-rule add/delete has happened yet — the normal
// PostMyExitRule / PostDeleteExitRule paths are the
// only places that re-run SetPolicy, and a code-only
// change (no data change) won't trigger them on its
// own.
//
// 2026-07-16: v0.13.0 — per-plane ACL. The reapply now
// iterates every distinct control plane (one entry per
// distinct headscale_url, plus the global default) and
// pushes the right policy to each. Single-plane deploys
// see the same single SetPolicy call as before.
//
// Admin-only. Idempotent.
package exit_rules

import (
	"fmt"
	"net/http"

	"skygate/internal/acl"
	"skygate/internal/headscale"
)

// PostAdminACLReapply regenerates the headscale policy
// from the current DB state and pushes it via
// SetPolicy. Admin-only. Idempotent.
// POST /admin/exit-rules/reapply
//
// The hsForPlane closure resolves the headscale_url to
// a cached *headscale.Client (same App.HSForUser /
// App.HSGlobal path the web and bot use), so the reapply
// pushes the right policy to every plane in one go. The
// alerter is the Service's Notifier (typed as
// acl.Alerter via the SendAlert signature).
func (s *Service) PostAdminACLReapply(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	// 2026-07-29: the per-plane closure used to call
	// a.HSGlobal() / a.HSForUser() (App methods that
	// don't exist on Service). Refactor-v0.30 Phase B
	// step 4e: route through s.ResolveHSForPlane (set
	// in cmd/skygate/main.go). The default behaviour
	// (also set in main.go) is: empty URL → HSGlobal(),
	// non-empty URL → first user_id with that
	// headscale_url → HSForUser(uid), fallback to
	// HSGlobal() on any error.
	results := acl.ApplyACLForAllPlanes(s.dbc(),
		func(planeURL string) *headscale.Client {
			if s.ResolveHSForPlane != nil {
				return s.ResolveHSForPlane(planeURL)
			}
			// Fallback when the callback isn't wired
			// (e.g. unit tests): use the Service's
			// own HS field (the global client).
			return s.HS
		},
		alerter,
		c.Username,
		fmt.Sprintf("reapply by %s (per-plane)", c.Username),
		viaFlag,
	)
	// v0.13.0 — single-plane deploys see one result, multi-
	// plane deploys see one per plane. Surface the first
	// failure (if any) and 200 on full success.
	for _, r := range results {
		if r.Err != nil {
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL reapply failed (by %s)\n  err: %v",
					c.Username, r.Err))
			}
			http.Error(w, "set policy: "+r.Err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if s.Notifier != nil {
		go s.Notifier.SendAlert(fmt.Sprintf("🔄 ACL reapply by %s → %d plane(s)", c.Username, len(results)))
	}
	http.Redirect(w, r, "/admin/exit-rules?reapplied=1", http.StatusSeeOther)
}
