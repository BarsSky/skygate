package admin

// admin_pages.go — read-only admin pages that were in
// internal/handlers/handlers_admin_pages.go.
//
//   - GetAdminAudit (/admin/audit — audit_log view, paginated DESC, with
//     optional ?action= and ?user= filters added 2026-07-11)
//   - GetAdminACLs  (/admin/acls — current headscale ACL policy view)
//
// refactor-v0.30 Phase B step 6b (2026-07-29): moved from
// internal/handlers/handlers_admin_pages.go (122 lines).

import (
	"net/http"
	"strings"
	"time"

	"skygate/internal/db"
)

// GetAdminAudit renders the audit_log view (paginated DESC, 200 rows).
// Filters: ?action= (exact) and ?user= (substring match on username).
func (s *Service) GetAdminAudit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
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
	actions, err := db.ListAuditActions(s.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Main query — apply the WHERE we built above.
	rows, err := s.DB.Query(`
		SELECT id, user_id, username, action, detail, created_at
		  FROM audit_log `+where+`
		 ORDER BY id DESC
		 LIMIT 200`, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	s.Backend.RenderWithLayout(w, r, "admin/audit.html", c, map[string]any{
		"Entries":      entries,
		"Actions":      actions,
		"ActionFilter": actionFilter,
		"UserFilter":   userFilter,
		"FilterActive": actionFilter != "" || userFilter != "",
	})
}

// GetAdminACLs renders the current headscale ACL policy view.
// When HEADPLANE_EXTERNAL_URL is set, link to the existing Headplane
// instead of the local sidecar (v0.10.12). The APIKey (redacted via
// the template's {{maskSecret}}) is passed so the operator can copy
// it into the headplane admin.
func (s *Service) GetAdminACLs(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hs := s.HSGlobalFn()
	policy, policyErr := hs.GetACL()
	errStr := ""
	if policyErr != nil {
		errStr = policyErr.Error()
	}
	// 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set,
	// link to the existing Headplane instead of the local sidecar.
	// The local sidecar URL is derived from ControlURL when no
	// external Headplane is configured.
	headplaneURL := s.HeadplaneExternalURL
	if headplaneURL == "" && s.ControlURL != "" {
		headplaneURL = s.ControlURL + "/admin/"
	}
	s.Backend.RenderWithLayout(w, r, "admin/acls.html", c, map[string]any{
		"Policy":       policy,
		"Error":        errStr,
		"HeadplaneURL": headplaneURL,
		"APIKey":       s.HeadscaleKey,
	})
}
