package db

import (
	"database/sql"
	"time"
)

// audit_log  —  helpers
//
// 2026-07-11: refactor v0.6.0 (Этап 9 part 2). The same INSERT was
// duplicated in handlers/handlers.go (a.audit), telegram/commands_phase3.go
// (/ack), and telegram/commands_phase4.go (/restart). Centralising here
// means a future column change (e.g. adding ip_address) doesn't require
// hunting three files.
//
// Two insert variants exist because the telegram helpers record events
// with username="telegram" and no portal user_id. Keeping the no-user-id
// variant explicit (rather than passing 0) avoids confusion at the call
// site and makes the SQL column list self-documenting.

const (
	// qInsertAuditLogNoUser is the 3-column variant used by telegram
	// commands (/ack, /restart) where there is no portal user_id.
	// Kept private to this file because no handler outside this file
	// needs it; AppendAuditLogNoUser is the typed wrapper.
	qInsertAuditLogNoUser = `INSERT INTO audit_log (username, action, detail) VALUES ($1, $2, $3)`

	// qDeleteAuditLogByUserID purges a user's audit history when the
	// portal user is deleted. Kept private to this file for the same
	// reason.
	qDeleteAuditLogByUserID = `DELETE FROM audit_log WHERE user_id = $1`
)

// AppendAuditLog writes one row to audit_log. userID is typically a
// portal_users.id, but 0 is acceptable for system events. The insert
// is best-effort from the caller's perspective — the App.audit wrapper
// intentionally ignores the returned error so a transient DB hiccup
// never breaks the main action (login, rule add, etc).
func AppendAuditLog(d *sql.DB, userID int64, username, action, detail string) error {
	_, err := d.Exec(qInsertAuditLog, userID, username, action, detail)
	return err
}

// AppendAuditLogNoUser writes one row to audit_log WITHOUT a user_id
// column. Use this from telegram /ack and /restart (the operator
// triggers the action from Telegram, not from a logged-in portal
// session, so there's no user_id to attach).
func AppendAuditLogNoUser(d *sql.DB, username, action, detail string) error {
	_, err := d.Exec(qInsertAuditLogNoUser, username, action, detail)
	return err
}

// DeleteAuditLogByUserID removes every audit_log row owned by `userID`.
// Called from the user-delete handler in a single transaction with
// preauth_keys + portal_users deletion. Errors are surfaced to the
// caller — unlike AppendAuditLog, this is part of an explicit flow
// where the operator wants feedback if the cleanup didn't land.
func DeleteAuditLogByUserID(d *sql.DB, userID int64) error {
	_, err := d.Exec(qDeleteAuditLogByUserID, userID)
	return err
}

// ListAuditActions returns the distinct action values present in
// audit_log, sorted alphabetically. Used by the /admin/audit page
// to populate the action filter dropdown. A small slice (a few dozen
// at most), so no LIMIT.
func ListAuditActions(d *sql.DB) ([]string, error) {
	rows, err := d.Query(qSelectAuditActions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AuditRow is a single row from the audit_log table, exposed to
// the per-user audit export handler (v0.25.1). Fields are kept
// as the wire types (string + int64 + time.Time) so the same
// struct works for both CSV and JSON output.
type AuditRow struct {
	ID         int64
	CreatedAt  time.Time
	UserID     int64
	Username   string
	Action     string
	Detail     string
}

// ListAuditLogForUser returns every audit_log row owned by
// `userID` (or rows where userID=0 but username matches `username`,
// e.g. /restart events logged by the bot on the user's behalf).
//
// v0.25.1 — exposed for the per-user CSV/JSON export. The
// `since` parameter is a unix timestamp (0 = no lower bound).
// Returns up to 10000 rows (cap to keep exports manageable;
// older rows are paginated via the `offset` parameter).
//
//   - if since > 0: filter created_at >= since
//   - if limit > 0: cap to `limit` rows (default 10000, hard max)
//   - if offset > 0: skip that many rows (pagination)
//
// Note: audit_log.created_at is stored as INTEGER (Unix seconds
// via strftime('%s','now')); we convert to time.Time here so
// the caller's downstream code can format however it likes
// (RFC3339 for the JSON export, RFC3339Nano for the CSV).
func ListAuditLogForUser(d *sql.DB, userID int64, username string, since int64, limit, offset int) ([]AuditRow, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT id, created_at, COALESCE(user_id, 0), username, action, COALESCE(detail, '')
	        FROM audit_log
	       WHERE (user_id = $1 OR (user_id = 0 AND username = $2))`
	args := []interface{}{userID, username}
	if since > 0 {
		q += ` AND created_at >= $3`
		args = append(args, since)
	}
	q += ` ORDER BY id DESC LIMIT $4 OFFSET $5`
	args = append(args, limit, offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var createdSec int64
		if err := rows.Scan(&r.ID, &createdSec, &r.UserID, &r.Username, &r.Action, &r.Detail); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdSec, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
