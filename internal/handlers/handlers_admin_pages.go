package handlers

// handlers_admin_pages.go — extracted from handlers.go.
// Admin pages that are read-only views:
// - GetAdminAudit (/admin/audit — audit_log view, paginated DESC, with
//   optional ?action= and ?user= filters added 2026-07-11)
// - GetAdminACLs  (/admin/acls — current headscale ACL policy view)

import (
	"net/http"
	"strings"
	"time"

	"skygate/internal/db"
)


func (a *App) GetAdminAudit(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	// 2026-07-11: read optional ?action= and ?user= filters so the
	// operator can scope to "telegram_ack", "user_create", or a
	// specific username without scrolling through 200 rows.
	q := r.URL.Query()
	actionFilter := strings.TrimSpace(q.Get("action"))
	userFilter := strings.TrimSpace(q.Get("user"))

	// Build the WHERE clause incrementally so empty filters don't
	// leave dangling ANDs.
	var (
		conds []string
		args  []any
	)
	if actionFilter != "" {
		conds = append(conds, "action = ?")
		args = append(args, actionFilter)
	}
	if userFilter != "" {
		// 2026-07-11: substring match on username — "alice" hits
		// "alice", "alice@..." etc. The exact match (`=`) is too
		// strict when operators are searching for a person.
		conds = append(conds, "username LIKE ?")
		args = append(args, "%"+userFilter+"%")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Distinct action list for the dropdown. Read first because
	// the operator needs it to pick a filter, and it's cheap
	// (a few dozen rows at most).
	// 2026-07-11: Этап 9 part 2 — moved to db.ListAuditActions
	actions, err := db.ListAuditActions(a.DB)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Main query — apply the WHERE we built above.
	rows, err := a.DB.Query(`
		SELECT id, user_id, username, action, detail, created_at
		  FROM audit_log `+where+`
		 ORDER BY id DESC
		 LIMIT 200`, args...)
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
		"Entries":       entries,
		"Actions":       actions,
		"ActionFilter":  actionFilter,
		"UserFilter":    userFilter,
		"FilterActive":  actionFilter != "" || userFilter != "",
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
	// 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set,
	// link to the existing Headplane instead of the local sidecar.
	// The local sidecar URL remains the default for backward compat.
	headplaneURL := a.HeadplaneExternalURL
	if headplaneURL == "" {
		headplaneURL = "https://tsnet.skynas.ru/admin/"
	}
	a.renderWithLayout(w, r, "admin/acls.html", c, map[string]any{
		"Policy":       policy,
		"Error":        errStr,
		"HeadplaneURL": headplaneURL,
		"APIKey":       a.HeadscaleKey,
	})
}
