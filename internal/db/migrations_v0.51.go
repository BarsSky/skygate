package db

import "database/sql"

// migrateV051 (v0.33.0, 2026-08-04): system_tests_runs table.
//
// Stores the history of test runs from the /admin/system_tests
// page (v0.33.0 Admin Test Page). Each row is one full test
// suite run: when it started, when it finished, and the JSON
// of all individual test results.
//
// Why we need this:
//
//   The test page shows results live (during a run) and the
//   operator wants to see history: "what was failing 6 hours
//   ago?" or "did the wal-g backup complete last night?".
//   Storing the result blob per run lets the page render a
//   trend column (pass count over time) and a per-test history
//   strip. We keep the last 100 runs (older ones pruned by
//   the page on insert).
//
// Schema (v0.33.0):
//
//   id                    INTEGER PK
//   started_at            INTEGER   strftime('%s','now') at insert
//   finished_at           INTEGER   strftime('%s','now') on completion
//   duration_ms           INTEGER   finished_at - started_at
//   results_json         TEXT      full {test_name: result} map
//   pass_count            INTEGER   # of {status: "pass"}
//   fail_count            INTEGER   # of {status: "fail"}
//   skip_count            INTEGER   # of {status: "skip"}
//   triggered_by_user_id  INTEGER   0 = scheduled, >0 = operator click
//
// 2026-08-04: v0.33.0 — Admin Test Page.
func migrateV051(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS system_tests_runs (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at             INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			finished_at            INTEGER NOT NULL DEFAULT 0,
			duration_ms            INTEGER NOT NULL DEFAULT 0,
			results_json           TEXT    NOT NULL DEFAULT '{}',
			pass_count             INTEGER NOT NULL DEFAULT 0,
			fail_count             INTEGER NOT NULL DEFAULT 0,
			skip_count             INTEGER NOT NULL DEFAULT 0,
			triggered_by_user_id   INTEGER NOT NULL DEFAULT 0
		)`,
		// Hot path: list recent runs.
		`CREATE INDEX IF NOT EXISTS idx_system_tests_runs_started
			ON system_tests_runs(started_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
