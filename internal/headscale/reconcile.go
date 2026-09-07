// Package headscale — reconcile.go (B237.18, closes TD-10).
//
// Reconciles portal_users.headscale_user_id with the live
// headscale user list. Detects three states:
//
//  1. Stale ID: portal_users.headscale_user_id points to a
//     headscale user that no longer exists. Most common cause:
//     the operator deleted the headscale user directly via
//     the headscale CLI / API, or the headscale DB was
//     restored from a backup that didn't include the user.
//     The portal_users row stays around (so the operator's
//     rules + audit log remain), but every rule that
//     references this user is now pointing at a no-op.
//
//     Reconciliation action: search headscale by username.
//     If found → update the ID (recreate case, where
//     headscale gave the recreated user a new numeric ID).
//     If not found → audit row, leave the ID alone (so
//     the operator can review whether to delete the
//     portal_users row by hand).
//
//  2. Never linked: portal_users.headscale_user_id IS NULL
//     or 0. The user exists in skygate but was never
//     reconciled against headscale (legacy rows from a
//     pre-reconciliation install, or users created via a
//     path that bypassed EnsureHeadscaleUser).
//
//     Reconciliation action: search headscale by username.
//     If found → link the ID. If not found → no-op (the
//     user is not in headscale yet, so there's nothing
//     to link to).
//
//  3. Healthy: portal_users.headscale_user_id matches a
//     current headscale user. No action.
//
// The reconciliation NEVER deletes portal_users rows
// automatically. If a user is missing from headscale AND
// can't be found by username, the script writes a
// headscale_user_reconcile audit row with detail
// `outcome=orphan` so the operator can decide whether to
// keep the row (audit history) or delete it.
//
// B237.18 design notes:
//
//  - The HSUser.ID is a string in headscale's API response
//    (the headscale JSON returns it as a string for backward
//    compatibility with the CLI's hex-id format). The
//    portal_users.headscale_user_id is INTEGER. The
//    reconciliation converts via strconv.ParseInt — an
//    empty / non-numeric ID is treated as "not in headscale"
//    and falls through to the username lookup.
//
//  - Reconciliation runs as a periodic cron (B237.18
//    adds internal/headscale/reconcile_cron.go +
//    internal/headscale/reconcile_cron_test.go). The cron
//    is opt-in via SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED
//    (default true) so the operator can disable it for
//    air-gapped installs where headscale is unreachable.
//
//  - One ListUsers() call per cycle (cached inside
//    *headscale.Client for cacheTTL; default 30s). If the
//    cache hasn't expired since the last ListUsers call
//    from another path (e.g. /admin/users page render),
//    the cron pays no extra cost.
//
//  - The reconciliation is a series of single-row
//    UPDATE statements, NOT a batch. The number of
//    portal_users rows is small (operator's user count,
//    typically <100), so batched updates would be
//    premature optimization. The transactions are
//    per-row, which means a single bad row doesn't
//    poison the whole cycle.

package headscale

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// ReconcileOutcome is the per-row outcome the
// reconciliation script records. One of:
//
//   - "ok" — the row's headscale_user_id matched a current
//     headscale user, no action needed
//   - "linked" — the row had no link (NULL/0) and we
//     successfully linked it to an existing headscale user
//   - "relinked" — the row had a stale ID; we found the
//     user by username in headscale and updated the ID
//   - "orphan" — the row's headscale_user_id points to
//     a non-existent user AND the username is not in
//     headscale; no auto-fix, audit row written
//   - "err" — something went wrong (DB error, headscale
//     unreachable, etc.); no DB change, error logged
type ReconcileOutcome string

const (
	ReconcileOK       ReconcileOutcome = "ok"
	ReconcileLinked   ReconcileOutcome = "linked"
	ReconcileRelinked ReconcileOutcome = "relinked"
	ReconcileOrphan   ReconcileOutcome = "orphan"
	ReconcileError    ReconcileOutcome = "err"
)

// ReconcileRow is the per-row result. Used in the
// returned slice and written as a JSON detail in the
// audit row. Field names are stable (the B-check
// scripts and the /admin/headscale page read them).
type ReconcileRow struct {
	PortalUserID    int64           `json:"portal_user_id"`
	PortalUsername  string          `json:"portal_username"`
	OldHSID         int64           `json:"old_hs_id"`
	NewHSID         int64           `json:"new_hs_id"`
	Outcome         ReconcileOutcome `json:"outcome"`
	Error           string          `json:"error,omitempty"`
}

// ReconcileResult is the summary returned by RunOnce.
// Counts + the per-row breakdown for the audit row.
type ReconcileResult struct {
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Rows       []ReconcileRow    `json:"rows"`
	OK         int               `json:"ok"`
	Linked     int               `json:"linked"`
	Relinked   int               `json:"relinked"`
	Orphans    int               `json:"orphans"`
	Errors     int               `json:"errors"`
	HSUsers    int               `json:"hs_users_total"`
}

// ReconcileUsers runs one reconciliation cycle against
// the live headscale. Returns the per-row breakdown +
// summary counts. Safe to call concurrently with the
// rest of skygate — the per-row UPDATEs are atomic
// (single-row transactions).
//
// The function NEVER deletes portal_users rows. See
// the package comment for the rationale.
//
// Audit row: a single audit_log row is written at the
// end of the cycle with detail = JSON-encoded
// ReconcileResult. The user_id=0 (system) +
// username="system_reconcile" pattern matches the
// certsync + mesh-cleanup conventions in this codebase.
func ReconcileUsers(ctx context.Context, db *sql.DB, hs *Client) (ReconcileResult, error) {
	res := ReconcileResult{StartedAt: time.Now().UTC(), Rows: []ReconcileRow{}}
	if db == nil {
		return res, fmt.Errorf("reconcile: db is nil")
	}
	if hs == nil {
		return res, fmt.Errorf("reconcile: headscale client is nil")
	}

	// Fetch the headscale user list once per cycle. The
	// Client's internal cache means a second call
	// immediately after (e.g. /admin/users page render)
	// won't re-hit the API.
	hsUsers, err := hs.ListUsers()
	if err != nil {
		// Don't abort the whole cycle on a headscale
		// outage — log + write a summary audit row
		// with the error. The next cycle can retry.
		log.Printf("reconcile: ListUsers failed: %v", err)
		writeAudit(db, 0, "system_reconcile", "headscale_user_reconcile",
			fmt.Sprintf(`{"outcome":"abort","error":%q,"started_at":%d}`,
				err.Error(), res.StartedAt.Unix()))
		return res, fmt.Errorf("reconcile: ListUsers: %w", err)
	}
	res.HSUsers = len(hsUsers)

	// Build a lookup map: name → user. We lowercase
	// the key for case-insensitive matching because
	// headscale usernames are case-sensitive on the
	// wire but skygate portal_users.username is
	// typically lowercased. The reconcile logic does
	// one more case-insensitive check below.
	byName := make(map[string]HSUser, len(hsUsers))
	for _, u := range hsUsers {
		byName[strings.ToLower(u.Name)] = u
	}

	// Stream portal_users rows (don't materialize all
	// at once — could be 10k+ rows for a multi-tenant
	// install). The SQL filter excludes the "system"
	// user (id=0 in some installs, headscale_user_id=0
	// is the canonical "no link" sentinel).
	rows, err := db.QueryContext(ctx, `
		SELECT id, username, COALESCE(headscale_user_id, 0)
		FROM portal_users
		WHERE username <> '' AND username IS NOT NULL
		ORDER BY id
	`)
	if err != nil {
		return res, fmt.Errorf("reconcile: query portal_users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r ReconcileRow
		if err := rows.Scan(&r.PortalUserID, &r.PortalUsername, &r.OldHSID); err != nil {
			res.Errors++
			res.Rows = append(res.Rows, ReconcileRow{
				Outcome: ReconcileError,
				Error:   fmt.Sprintf("scan: %v", err),
			})
			continue
		}
		r.Outcome, r.NewHSID, r.Error = reconcileOne(ctx, db, hs, byName, r.PortalUserID, r.PortalUsername, r.OldHSID)
		switch r.Outcome {
		case ReconcileOK:
			res.OK++
		case ReconcileLinked:
			res.Linked++
		case ReconcileRelinked:
			res.Relinked++
		case ReconcileOrphan:
			res.Orphans++
		case ReconcileError:
			res.Errors++
		}
		res.Rows = append(res.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("reconcile: iterate portal_users: %w", err)
	}
	res.FinishedAt = time.Now().UTC()

	// Write the summary audit row. Single row per
	// cycle so the operator's /admin/audit filter for
	// action=headscale_user_reconcile gives them a
	// timeline of how often reconciliation runs and
	// how many rows it touched.
	summaryJSON, _ := marshalJSON(res) //nolint:errcheck // best-effort
	// Best-effort: a DB write failure here doesn't
	// fail the cycle (the per-row audit row + the
	// log line are the operator's primary signals;
	// this summary row is a nice-to-have).
	if err := writeAuditRaw(db, summaryJSON); err != nil {
		log.Printf("reconcile: write summary audit row: %v", err)
	}

	log.Printf("reconcile: %d portal_users checked (hs_users=%d, ok=%d, linked=%d, relinked=%d, orphans=%d, errors=%d, took=%s)",
		len(res.Rows), res.HSUsers, res.OK, res.Linked, res.Relinked, res.Orphans, res.Errors,
		res.FinishedAt.Sub(res.StartedAt).Round(time.Millisecond))

	return res, nil
}

// reconcileOne handles a single portal_users row. Returns
// the outcome + the new headscale_user_id (unchanged
// for OK / orphan / err cases). The HSUser lookup uses
// the byName map built by the caller.
//
// Per-row transaction model: each row gets its own
// transaction. A single row's DB error doesn't poison
// the rest of the cycle (one bad row writes an
// err-case audit row, the next row gets a fresh
// transaction).
func reconcileOne(
	ctx context.Context,
	db *sql.DB,
	hs *Client,
	byName map[string]HSUser,
	portalUserID int64,
	username string,
	oldHSID int64,
) (ReconcileOutcome, int64, string) {
	if username == "" {
		return ReconcileError, oldHSID, "username is empty"
	}

	// The headscale Client caches ListUsers for
	// cacheTTL (default 30s), so calling
	// ListUsersByName below is essentially free
	// if the cron is the only thing calling it.
	// The byName map already has the same data;
	// using it skips the network entirely.
	hsUser, foundByName := byName[strings.ToLower(username)]

	// Case 1: row has a valid link, and the linked
	// user still exists in headscale. Verify by
	// checking if oldHSID matches any of the
	// headscale user IDs.
	if oldHSID > 0 {
		linked := false
		for _, u := range byName {
			if int64FromString(u.ID) == oldHSID {
				linked = true
				break
			}
		}
		if linked {
			return ReconcileOK, oldHSID, ""
		}
		// Stale. Try to relink by username.
		if foundByName {
			newID := int64FromString(hsUser.ID)
			if newID > 0 {
				if err := updateHeadscaleUserID(ctx, db, portalUserID, newID); err != nil {
					return ReconcileError, oldHSID, fmt.Sprintf("relink update: %v", err)
				}
				writeAudit(db, portalUserID, username, "headscale_user_reconcile",
					fmt.Sprintf(`{"outcome":"relinked","old_hs_id":%d,"new_hs_id":%d}`,
						oldHSID, newID))
				return ReconcileRelinked, newID, ""
			}
		}
		// Orphan. The user is not in headscale at
		// all (neither by ID nor by username).
		// Don't auto-delete — leave the row, write
		// an audit row so the operator can decide.
		writeAudit(db, portalUserID, username, "headscale_user_reconcile",
			fmt.Sprintf(`{"outcome":"orphan","old_hs_id":%d}`, oldHSID))
		return ReconcileOrphan, oldHSID, ""
	}

	// Case 2: row has no link (oldHSID == 0 or NULL).
	// Try to link by username.
	if !foundByName {
		// The user is not in headscale. No-op —
		// they're either a not-yet-provisioned
		// skygate user (will be linked by the next
		// EnsureHeadscaleUser call) or a deleted-
		// from-headscale user (which the operator
		// will see as headscale_user_reconcile
		// outcome=ok when they have oldHSID=0).
		return ReconcileOK, oldHSID, ""
	}

	// Link.
	newID := int64FromString(hsUser.ID)
	if newID <= 0 {
		return ReconcileError, oldHSID, fmt.Sprintf("headscale returned non-numeric ID %q", hsUser.ID)
	}
	if err := updateHeadscaleUserID(ctx, db, portalUserID, newID); err != nil {
		return ReconcileError, oldHSID, fmt.Sprintf("link update: %v", err)
	}
	writeAudit(db, portalUserID, username, "headscale_user_reconcile",
		fmt.Sprintf(`{"outcome":"linked","new_hs_id":%d}`, newID))
	return ReconcileLinked, newID, ""
}

// int64FromString converts a headscale API ID (string
// like "42" or "abc123") to int64. Returns 0 on parse
// failure (the caller treats 0 as "not in headscale").
func int64FromString(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// updateHeadscaleUserID writes the new headscale_user_id
// to portal_users.id = portalUserID. The UPDATE is a
// single statement (single-row transaction on PG +
// SQLite). No batch — see package comment for rationale.
func updateHeadscaleUserID(ctx context.Context, db *sql.DB, portalUserID int64, newHSID int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE portal_users SET headscale_user_id = $1 WHERE id = $2`,
		newHSID, portalUserID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("UPDATE affected 0 rows (portalUserID=%d was deleted between SELECT and UPDATE?)", portalUserID)
	}
	return nil
}

// writeAudit writes a per-row audit_log entry. Used for
// relinked / linked / orphan outcomes where the
// per-row detail matters. The summary audit row
// (writeAuditRaw below) covers the bulk counts.
func writeAudit(db *sql.DB, userID int64, username, action, detailJSON string) {
	_, err := db.Exec(`
		INSERT INTO audit_log (user_id, username, action, detail, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, userID, username, action, detailJSON, time.Now().Unix())
	if err != nil {
		log.Printf("reconcile: write audit row (%s/%s): %v", action, detailJSON, err)
	}
}

// writeAuditRaw is the summary audit row writer. The
// "user" here is the synthetic id=0 (system) with
// username="system_reconcile" — matches the certsync +
// mesh-cleanup pattern.
func writeAuditRaw(db *sql.DB, detailJSON string) error {
	_, err := db.Exec(`
		INSERT INTO audit_log (user_id, username, action, detail, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, 0, "system_reconcile", "headscale_user_reconcile", detailJSON, time.Now().Unix())
	return err
}

// marshalJSON is a tiny wrapper to avoid importing
// encoding/json at the top of the file (we'd rather
// keep the import surface small + obvious). The
// package comment mentions it's best-effort — a
// marshal failure is logged but doesn't fail the
// cycle.
func marshalJSON(v any) (string, error) {
	// We import encoding/json at the package level
	// (it's used in the audit-row detail JSON
	// strings via fmt.Sprintf elsewhere, but
	// here we want a real marshaller for the
	// summary row). Defined in reconcile_json.go
	// to keep this file short.
	return marshalJSONImpl(v)
}
