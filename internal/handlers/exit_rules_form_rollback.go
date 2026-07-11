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
	var config string
	if err := a.DB.QueryRow("SELECT config FROM acl_snapshots WHERE version = ?", ver).Scan(&config); err != nil {
		http.Error(w, "version not found", 404)
		return
	}
	if err := a.HS.SetPolicy(config); err != nil {
		a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'rollback_fail', ?)", ver, err.Error())
		http.Error(w, err.Error(), 500)
		return
	}
	a.saveACLSnapshot(config, c.Username)
	a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'rollback', ?)", ver, fmt.Sprintf("rolled back by %s", c.Username))
	http.Redirect(w, r, "/admin/exit-rules?rolled=1", http.StatusFound)
}
