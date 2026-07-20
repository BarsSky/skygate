// v0.20.0 — headscale_releases table for the
// /admin/headscale page + headscale-update-monitor.
//
// The v0.18.1 deploy surfaced three operator
// concerns that the v0.20.0 headscale-update-monitor
// addresses:
//
//   1. There's no in-app way to see "is there a
//      newer headscale than what I run?". The
//      operator has to GitHub-stalk the project
//      or wait for a Tailscale user to notice a
//      warning in the log. v0.20.0 adds a
//      background monitor (see
//      internal/headscale_version/) that polls
//      GitHub Releases and surfaces the result on
//      /admin/headscale + a banner on
//      /admin/exit-nodes + a /headscale bot
//      command.
//
//   2. There's no audit trail of "which headscale
//      versions did skygate know about when?". The
//      monitor's only output was a Telegram
//      message — gone after the message scrolls
//      out of the chat. The headscale_releases
//      table persists every seen version (one row
//      per unique tag) so the admin page has a
//      history view.
//
//   3. The dedup logic in the monitor needs a
//      "notified" flag so a successful alert
//      doesn't get re-sent on the next tick if
//      the operator hasn't changed the pinned
//      version yet. The column is a tiny boolean
//      but having it in the table means the
//      admin page can show "headscale 0.30.0 was
//      seen on 2026-08-15, alert sent on
//      2026-08-15" without needing a second
//      audit_log query.
//
// Schema:
//
//   headscale_releases
//     (version TEXT PRIMARY KEY,
//      published_at INTEGER NOT NULL,  -- unix seconds; 0 if unknown
//      first_seen_at INTEGER NOT NULL,
//      html_url TEXT NOT NULL DEFAULT '',
//      name TEXT NOT NULL DEFAULT '',
//      body TEXT NOT NULL DEFAULT '',
//      is_breaking INTEGER NOT NULL DEFAULT 0,  -- 0/1
//      notified INTEGER NOT NULL DEFAULT 0)     -- 0/1
//
// PRIMARY KEY on version means the monitor's
// INSERT OR IGNORE is a no-op for already-seen
// tags, so a re-poll from a different pinned
// version doesn't create duplicates.
//
// No foreign keys: the table is a snapshot cache,
// not a reference to any other entity. The
// operator can DELETE rows to clear the history
// (the monitor will re-add them on the next
// tick).
package db

import "database/sql"

// migrationV041 — v0.20.0 headscale-update-monitor.
//
// Idempotent: re-runs are no-ops (CREATE TABLE
// IF NOT EXISTS, CREATE INDEX IF NOT EXISTS).
func migrationV041(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS headscale_releases (
			version TEXT PRIMARY KEY,
			published_at INTEGER NOT NULL DEFAULT 0,
			first_seen_at INTEGER NOT NULL DEFAULT 0,
			html_url TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			is_breaking INTEGER NOT NULL DEFAULT 0,
			notified INTEGER NOT NULL DEFAULT 0
		)`,
		// Index on published_at DESC for the
		// /admin/headscale "history" view (newest
		// first). The PRIMARY KEY covers exact
		// lookups; the index covers the range scan
		// the page renders.
		`CREATE INDEX IF NOT EXISTS idx_headscale_releases_published
			ON headscale_releases (published_at DESC)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
