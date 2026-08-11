// Package exit_rules — form_rollback.go owns the admin
// ACL rollback handler.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved from
// internal/handlers/exit_rules_form_rollback.go. Restores
// a previously-saved acl_snapshots row as the live
// headscale policy. Admin-only.
package exit_rules

import (
	"fmt"
	"net/http"
	"strconv"

	"skygate/internal/db"
)

// PostAdminRollbackACL is the POST handler for the
// rollback button on /admin/exit-rules. Admin-only.
// POST /admin/exit-rules/rollback
func (s *Service) PostAdminRollbackACL(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	verStr := r.FormValue("version")
	ver, _ := strconv.Atoi(verStr)
	if ver == 0 {
		http.Error(w, "invalid version", http.StatusBadRequest)
		return
	}
	config, err := db.GetACLConfig(s.DB, ver)
	if err != nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}
	if err := s.HS.SetPolicy(config); err != nil {
		db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionRollbackFail, err.Error())
		// 2026-07-11: rollback failure is loud — admin tried to restore
		// a known-good policy and the headscale API rejected it. Pager time.
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL rollback failed (by %s, target v%d)\n  err: %v",
				c.Username, ver, err))
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.saveACLSnapshot(config, c.Username)
	db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionRollback, fmt.Sprintf("rolled back by %s", c.Username))
	if s.Notifier != nil {
		go s.Notifier.SendAlert(fmt.Sprintf("⏪ ACL rollback by %s → v%d", c.Username, ver))
	}
	http.Redirect(w, r, "/admin/exit-rules?rolled=1", http.StatusFound)
}
