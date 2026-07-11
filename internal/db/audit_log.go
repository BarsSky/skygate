package db

import (
	"database/sql"
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
	qInsertAuditLogNoUser = `INSERT INTO audit_log (username, action, detail) VALUES (?, ?, ?)`

	// qDeleteAuditLogByUserID purges a user's audit history when the
	// portal user is deleted. Kept private to this file for the same
	// reason.
	qDeleteAuditLogByUserID = `DELETE FROM audit_log WHERE user_id = ?`
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
