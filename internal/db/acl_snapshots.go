package db

import (
	"database/sql"
)

// 2026-07-11: refactor v0.6.0 (Этап 9). The acl_snapshots table is the
// headscale policy BLOB store: every successful GenerateACL() produces one
// row, plus a row is created for every "save attempt" that failed (with
// applied_success=0 and error_msg set). The previous inline pattern was
// repeated in 4 files:
//
//	a.DB.QueryRow("SELECT COALESCE(MAX(version),0) FROM acl_snapshots").Scan(&maxVer)
//	a.DB.Exec("INSERT INTO acl_snapshots (version, config, ...) VALUES (?, ?, ?, 1)", ver, ...)
//	a.DB.Exec("UPDATE acl_snapshots SET applied_success=1 WHERE version=?", ver)
//	a.DB.Exec("UPDATE acl_snapshots SET applied_success=0, error_msg=? WHERE version=?", err, ver)
//	a.DB.QueryRow("SELECT config FROM acl_snapshots WHERE version = ?", ver).Scan(&config)
//
// This file replaces all 5 patterns with typed helpers. The intent is
// not to invent a new ACL abstraction (headscale owns the schema) —
// just to take the column-shape dependency out of the handler layer.

// NextACLVersion returns the next version number to use for a new
// acl_snapshots row (= max(version) + 1, or 1 if the table is empty).
//
// Caveat: this is racy. With SQLite + a single open connection (the
// skygate default — SetMaxOpenConns(1)) the race is closed in practice
// because the connection serializes writes. If a future deployment runs
// with multiple skygate instances, wrap saveACLSnapshot in a transaction
// or move to a SERIALIZABLE-isolation store.
func NextACLVersion(d *sql.DB) (int, error) {
	var max int
	err := d.QueryRow(qMaxACLVersion).Scan(&max)
	if err != nil {
		return 0, err
	}
	return max + 1, nil
}

// SaveACLSnapshot inserts a new acl_snapshots row marked already-applied
// (applied_success=1) and returns the version number that was assigned.
// Callers must use NextACLVersion(d) before calling this if they need a
// specific version number to thread through; the helper itself does
// NOT consult NextACLVersion because the surrounding code (the
// GenerateACL → SetPolicy pipeline) wants the version available up
// front so it can be embedded in log lines and audit events.
//
// `createdBy` is the username (or bot name) that produced this policy.
func SaveACLSnapshot(d *sql.DB, version int, config, createdBy string) error {
	_, err := d.Exec(qInsertACLSnapshot, version, config, createdBy)
	return err
}

// MarkACLApplied flips applied_success to 1 for `version`. Called after
// headscale has accepted the policy. The error is returned so callers
// can log; in practice the call sites still wrap in `_, _ = ...` because
// the user-visible request has already succeeded at that point.
func MarkACLApplied(d *sql.DB, version int) error {
	_, err := d.Exec(qMarkACLApplied, version)
	return err
}

// MarkACLFail records applied_success=0 and the error message for `version`.
// `errMsg` is typically err.Error() from the headscale call. Empty
// errMsg is allowed — the column accepts "".
func MarkACLFail(d *sql.DB, version int, errMsg string) error {
	_, err := d.Exec(qMarkACLFail, errMsg, version)
	return err
}

// GetACLConfig reads the headscale HuJSON policy for `version`. Returns
// sql.ErrNoRows if no such version exists. Used by the rollback handler
// to feed a previous policy back to headscale.
func GetACLConfig(d *sql.DB, version int) (string, error) {
	var config string
	err := d.QueryRow(qSelectACLConfig, version).Scan(&config)
	return config, err
}

// LastAppliedACLVersion returns the highest version with
// applied_success=1, or 0 if none. Powers the telegram /status command
// ("last successful ACL: v7").
func LastAppliedACLVersion(d *sql.DB) (int, error) {
	var v int
	err := d.QueryRow(qLastAppliedACLVersion).Scan(&v)
	return v, err
}

// ACLSnapshot is the row shape used by the admin /admin/exit-rules page
// bottom panel. Mirrors the SELECT in qSelectRecentACLSnapshots.
type ACLSnapshot struct {
	Version        int
	CreatedBy      string
	AppliedSuccess sql.NullInt64 // NULL=pending, 0=failed, 1=ok
	ErrorMsg       string
	CreatedAt      int64
}

// RecentACLSnapshots returns the latest N snapshots, newest first.
// Used by /admin/exit-rules.
func RecentACLSnapshots(d *sql.DB) ([]ACLSnapshot, error) {
	rows, err := d.Query(qSelectRecentACLSnapshots)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ACLSnapshot
	for rows.Next() {
		var s ACLSnapshot
		if err := rows.Scan(&s.Version, &s.CreatedBy, &s.AppliedSuccess, &s.ErrorMsg, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
