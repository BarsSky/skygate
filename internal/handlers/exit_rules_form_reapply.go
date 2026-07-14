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
// Admin-only. Idempotent.

import (
	"fmt"
	"net/http"

	"skygate/internal/db"
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
	acl, err := a.GenerateACL()
	if err != nil {
		http.Error(w, "generate acl: "+err.Error(), 500)
		return
	}
	ver := a.saveACLSnapshot(acl, c.Username)
	if err := a.HS.SetPolicy(acl); err != nil {
		db.AppendExitRuleLog(a.DB, ver, db.ExitRuleActionApplyFail,
			fmt.Sprintf("reapply by %s failed: %v", c.Username, err))
		if a.Notifier != nil {
			go a.Notifier.SendAlert(fmt.Sprintf("❌ ACL reapply failed (by %s)\n  err: %v",
				c.Username, err))
		}
		http.Error(w, "set policy: "+err.Error(), 500)
		return
	}
	db.MarkACLApplied(a.DB, ver)
	db.AppendExitRuleLog(a.DB, ver, db.ExitRuleActionApply,
		fmt.Sprintf("reapply by %s → v%d", c.Username, ver))
	if a.Notifier != nil {
		go a.Notifier.SendAlert(fmt.Sprintf("🔄 ACL reapply by %s → v%d", c.Username, ver))
	}
	http.Redirect(w, r, "/admin/exit-rules?reapplied=1", http.StatusSeeOther)
}
