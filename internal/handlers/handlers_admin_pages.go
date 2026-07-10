package handlers

// handlers_admin_pages.go — extracted from handlers.go.
// Admin pages that are read-only views:
// - GetAdminAudit (/admin/audit — audit_log view, paginated DESC)
// - GetAdminACLs  (/admin/acls — current headscale ACL policy view)

import (
	"net/http"
	"time"
)


func (a *App) GetAdminAudit(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	rows, err := a.DB.Query(`SELECT id, user_id, username, action, detail, created_at FROM audit_log ORDER BY id DESC LIMIT 200`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type Entry struct {
		ID               int64
		UserID           int64
		Username, Action string
		Detail           string
		Time             string
	}
	var entries []Entry
	for rows.Next() {
		var e Entry
		var t int64
		_ = rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Detail, &t)
		e.Time = time.Unix(t, 0).Format("2006-01-02 15:04:05")
		entries = append(entries, e)
	}
	a.renderWithLayout(w, r, "admin/audit.html", c, map[string]any{
		"Entries": entries,
	})
}

func (a *App) GetAdminACLs(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	policy, policyErr := a.HS.GetACL()
	errStr := ""
	if policyErr != nil {
		errStr = policyErr.Error()
	}
	a.renderWithLayout(w, r, "admin/acls.html", c, map[string]any{
		"Policy":       policy,
		"Error":        errStr,
		"HeadplaneURL": "https://tsnet.skynas.ru/admin/",
		"APIKey":       a.HeadscaleKey,
	})
}
