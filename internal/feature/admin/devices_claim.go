// internal/feature/admin/devices_claim.go — bulk-claim handler
// (B-mod-first-run-adoption T4-T5).
//
// The bulk-claim handler is the operator's escape hatch when
// nodes joined headscale via `headscale preauthkeys create` (NOT
// via skygate's /my/preauth) and never got auto-attributed by
// nodeownership.Backfill (Strategies A/C/D/E all need a
// preauth key or existing tag to match). The operator goes to
// /admin/users/{id}, clicks "Claim all nodes for this user", and
// every node_owner_map row matching that headscale user gets
// its tagged_by_user_id set to the portal user.
//
// Idempotency: re-running on already-claimed rows is a no-op (the
// UPDATE WHERE clause filters out rows already tagged by this
// portal user). The pre-existing "Sync from headscale" button is
// still the recommended path for most operators — bulk-claim is
// only for the "I imported headscale users as portal users, now
// I need to attach their existing nodes" case.
package admin

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"skygate/internal/db"
)

// PostAdminDevicesClaimAllForUser assigns every node_owner_map row
// whose headscale_user_id matches the portal user's
// headscale_user_id AND whose tagged_by_user_id is currently NULL
// (or a different user) to the given portal user. Admin-only.
//
// Form input: portal_user_id (required, integer).
//
// Side effects:
//   - UPDATE node_owner_map SET tagged_by_user_id = ? WHERE
//     headscale_user_id = (portal_user.headscale_user_id) AND
//     (tagged_by_user_id IS NULL OR tagged_by_user_id != ?)
//   - audit_log row with action='claim_all_for_user'
//
// Returns 303 redirect to /admin/users/{id} on success, 400 on
// invalid input, 403 for non-admin callers, 500 on DB errors.
func (s *Service) PostAdminDevicesClaimAllForUser(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	portalUserIDStr := r.FormValue("portal_user_id")
	portalUserID, err := strconv.ParseInt(portalUserIDStr, 10, 64)
	if err != nil || portalUserID <= 0 {
		http.Error(w, "portal_user_id required (positive integer)", http.StatusBadRequest)
		return
	}

	conn := s.DB.Current()
	if conn == nil {
		http.Error(w, "db not available", http.StatusInternalServerError)
		return
	}

	// 1. Resolve the portal user's headscale_user_id. If the
	// portal user is not linked to a headscale user, there are
	// no nodes to claim — return success (no-op).
	var headscaleUserID sql.NullInt64
	if err := conn.QueryRowContext(r.Context(),
		`SELECT headscale_user_id FROM portal_users WHERE id = ?`,
		portalUserID,
	).Scan(&headscaleUserID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("portal user %d not found", portalUserID), http.StatusNotFound)
			return
		}
		log.Printf("[claim-all-for-user] lookup portal_users: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if !headscaleUserID.Valid {
		// Portal user has no headscale link — nothing to claim.
		// Still write the audit row so the operator sees the
		// attempt in /admin/audit.
		_ = db.AppendAuditLogWithTarget(conn, c.UserID, c.Username, "claim_all_for_user",
			fmt.Sprintf(`{"portal_user_id":%d,"claimed":0,"note":"portal_user_not_linked_to_headscale"}`, portalUserID),
			"portal_user", fmt.Sprint(portalUserID))
		http.Redirect(w, r, fmt.Sprintf("/admin/users/%d", portalUserID), http.StatusSeeOther)
		return
	}

	// 2. UPDATE node_owner_map rows for this headscale user
	// that aren't already tagged by this portal user. The
	// filter (tagged_by_user_id IS NULL OR tagged_by_user_id != ?)
	// makes the operation idempotent.
	res, err := conn.ExecContext(r.Context(),
		`UPDATE node_owner_map
		    SET tagged_by_user_id = ?
		  WHERE headscale_user_id = ?
		    AND (tagged_by_user_id IS NULL OR tagged_by_user_id != ?)`,
		portalUserID, headscaleUserID.Int64, portalUserID,
	)
	if err != nil {
		log.Printf("[claim-all-for-user] UPDATE node_owner_map: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	claimed, _ := res.RowsAffected()

	// 3. Audit row. B221+ AppendAuditLogWithTarget takes
	// (d, userID, username, action, detail, targetType, targetID).
	// target_type = "portal_user", target_id = the portal user id
	// (so the /admin/audit page can deep-link to /admin/users/{id}).
	detail := fmt.Sprintf(
		`{"portal_user_id":%d,"headscale_user_id":%d,"claimed":%d}`,
		portalUserID, headscaleUserID.Int64, claimed,
	)
	if err := db.AppendAuditLogWithTarget(conn, c.UserID, c.Username, "claim_all_for_user", detail,
		"portal_user", fmt.Sprint(portalUserID)); err != nil {
		// Don't fail the operator's request on audit failure —
		// the claim already succeeded, just the audit row is
		// missing. Log it for follow-up.
		log.Printf("[claim-all-for-user] AppendAuditLogWithTarget: %v (claim succeeded; audit row missing)", err)
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d", portalUserID), http.StatusSeeOther)
}
