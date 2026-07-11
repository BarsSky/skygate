package handlers

// exit_rules_form_rollback.go — admin ACL rollback.
// - PostAdminRollbackACL (POST /admin/exit-rules/rollback)
//
// Restores a previously-saved acl_snapshots row as the live headscale
// policy. Admin-only.

import (
	"fmt"
	"net/http"
	"strconv"

	"skygate/internal/db"
)



func (a *App) PostAdminRollbackACL(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	verStr := r.FormValue("version")
	ver, _ := strconv.Atoi(verStr)
	if ver == 0 {
		http.Error(w, "invalid version", 400)
		return
	}
	config, err := db.GetACLConfig(a.DB, ver)
	if err != nil {
		http.Error(w, "version not found", 404)
		return
	}
	if err := a.HS.SetPolicy(config); err != nil {
		db.AppendExitRuleLog(a.DB, ver, db.ExitRuleActionRollbackFail, err.Error())
		// 2026-07-11: rollback failure is loud — admin tried to restore
		// a known-good policy and the headscale API rejected it. Pager time.
		if a.Notifier != nil {
			go a.Notifier.SendAlert(fmt.Sprintf("❌ ACL rollback failed (by %s, target v%d)\n  err: %v",
				c.Username, ver, err))
		}
		http.Error(w, err.Error(), 500)
		return
	}
	a.saveACLSnapshot(config, c.Username)
	db.AppendExitRuleLog(a.DB, ver, db.ExitRuleActionRollback, fmt.Sprintf("rolled back by %s", c.Username))
	if a.Notifier != nil {
		go a.Notifier.SendAlert(fmt.Sprintf("⏪ ACL rollback by %s → v%d", c.Username, ver))
	}
	http.Redirect(w, r, "/admin/exit-rules?rolled=1", http.StatusFound)
}
