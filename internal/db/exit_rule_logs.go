package db

import (
	"database/sql"
	"time"
)

// 2026-07-11: refactor v0.6.0 (Этап 9). The exit_rule_logs table is the
// append-only audit log of every ACL action — "apply", "delete", "sync",
// "rollback", "apply_fail", "delete_fail", "rollback_fail", "autoupdate",
// "api_bulk" — and there are 10+ insert sites across exit_rules_form_my.go,
// exit_rules_form_rollback.go, exit_rules_sync.go, exit_rules_api.go and
// exit_rules_form_my.go's delete path. The previous pattern was:
//
//	a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'apply', ?)", ver, msg)
//
// It worked, but two things bit us in practice:
//
//   1. The action string was hard-coded inline at every call site. A typo
//      ('aplly' instead of 'apply') would land in the DB and the admin
//      filter dropdown would never surface that row, making the bug
//      silent. With the helper, the action is a typed string parameter
//      and a constant table of valid values lives next to it.
//
//   2. The "version" column is sometimes 0 for autoupdate logs (which
//      don't correspond to a specific ACL snapshot). Forgetting that
//      detail and passing a stale `ver` is a common copy-paste hazard;
//      the helper makes 0 an explicit, documented "no specific version"
//      value via the const ExitRuleLogNoVersion.
//
// AppendExitRuleLog is a best-effort helper: it returns the error so
// callers can log it, but the call sites still use `_, _ = db.Exec(...)`
// or just `db.Exec(...)` because log writes must NEVER block the user
// request. The helper's main job is to make the call sites readable and
// to give us one place to evolve the schema (e.g. add a `user_id` column
// later, or change the action enum).

// ExitRuleLogNoVersion is the canonical "no associated ACL snapshot" value
// for the version column. Used by autoupdate entries, which fire from a
// timer and don't correspond to a specific user-initiated ACL change.
const ExitRuleLogNoVersion = 0

// Standard exit_rule_logs.action values. Centralising them here gives
// IDE completion and makes the "is this value valid?" question answerable
// in one place.
const (
	ExitRuleActionApply        = "apply"
	ExitRuleActionDelete       = "delete"
	ExitRuleActionSync         = "sync"
	ExitRuleActionRollback     = "rollback"
	ExitRuleActionApplyFail    = "apply_fail"
	ExitRuleActionDeleteFail   = "delete_fail"
	ExitRuleActionRollbackFail = "rollback_fail"
	ExitRuleActionAutoupdate   = "autoupdate"
	ExitRuleActionAPIBulk      = "api_bulk"
)

// AppendExitRuleLog writes one row to exit_rule_logs.
//
// `version` is the associated acl_snapshots.version, or ExitRuleLogNoVersion
// (0) for autoupdate events. `action` should be one of the ExitRuleAction*
// constants; the helper does not validate the value, but the constants
// exist so the call sites don't have to memorise the strings.
//
// `detail` is free-form (typically a "user X added rule Y" sentence) and
// can be empty. The string is stored verbatim — no escaping needed since
// the SQLite driver handles parameter binding.
func AppendExitRuleLog(d *sql.DB, version int, action, detail string) error {
	_, err := d.Exec(qInsertExitRuleLog, version, action, detail)
	return err
}

// LastSyncForExitNode returns the unix timestamp of the most recent
// "sync" log line that mentions the given exit_node name in its detail
// (LIKE-match, so a detail like "synced 0.0.0.0/0 to exit-node-1" will
// match a query for "exit-node-1"). Returns 0 if no sync has ever
// happened for this node.
//
// Used by the admin /admin/exit-rules/nodes page to render the
// "Last sync" column. Returns 0 (not an error) when there is no row,
// which the caller renders as "никогда".
func LastSyncForExitNode(d *sql.DB, exitNode string) (int64, error) {
	var ts int64
	err := d.QueryRow(qSelectLastSyncForExitNode, "%"+exitNode+"%").Scan(&ts)
	return ts, err
}

// ExitRuleLog is the row shape used by the admin /admin/exit-rules page
// top panel. Mirrors the SELECT in qSelectRecentExitRuleLogs.
type ExitRuleLog struct {
	Version   int
	Action    string
	Detail    string
	CreatedAt int64
}

// RecentExitRuleLogs returns the latest N log lines (newest first).
// Used by /admin/exit-rules. `limit` is bounded by the SQL itself, so
// we don't add a Go-side cap.
func RecentExitRuleLogs(d *sql.DB) ([]ExitRuleLog, error) {
	rows, err := d.Query(qSelectRecentExitRuleLogs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExitRuleLog
	for rows.Next() {
		var l ExitRuleLog
		if err := rows.Scan(&l.Version, &l.Action, &l.Detail, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ExitRuleLogTime formats an exit_rule_logs row's created_at column
// (unix seconds) for display. Returns "" for the zero value.
func ExitRuleLogTime(unixSec int64) string {
	if unixSec <= 0 {
		return ""
	}
	return time.Unix(unixSec, 0).Format("2006-01-02 15:04:05")
}
