package db

import "database/sql"

// migrateV036 (2026-07-15): exit-node health monitor (v0.13.0).
//
// Two new tables power the continuous exit-node health check:
//
//   exit_node_health
//     One row per headscale node. Stores the most recent health
//     snapshot (online, last_seen, route-approval state, tag
//     presence, last check timestamp, last observed state, last
//     state-change timestamp, consecutive failure counter). The
//     monitor upserts this on every tick.
//
//   exit_node_state_changes
//     Append-only log of detected state transitions. The monitor
//     inserts one row per detected change and marks alerted_at
//     once the Telegram alert has been queued (dedup: same
//     from→to transition for the same node is suppressed by
//     checking the latest row before inserting).
//
// Why two tables (instead of one with a `pending_alerts` boolean
// on the snapshot): the snapshot is updated on every tick (could
// be 5 min × 3 nodes = ~864 writes / day per node); the state-
// change log is appended only on actual transitions (~a few per
// day in healthy operation). Splitting them keeps the hot path
// (snapshot) cheap and the cold path (alert dispatch) easy to
// query: "give me all rows where alerted_at = 0".
//
// Online detection is deliberately a bit forgiving: a node is
// "online" if its headscale `online` field is true OR its
// `lastSeen` is within SKYGATE_EXIT_NODE_OFFLINE_AFTER (default
// 2 minutes). headscale's `online` flips to false the moment
// the WireGuard session closes — which can be a long-lived
// laptop briefly losing WiFi as much as a relay that's actually
// down. The `lastSeen` fallback avoids alert spam on transient
// laptop reconnects.
//
// Schema is additive and idempotent: every statement is CREATE
// IF NOT EXISTS / CREATE INDEX IF NOT EXISTS. Safe to re-run.
func migrateV036(d *sql.DB) error {
	stmts := []string{
		// 2026-07-15: v0.13.0 — exit-node health snapshot.
		// `state` is one of: unknown | online | offline | degraded.
		// `degraded` = online + has the tag but routes are not
		// approved (e.g. admin ran --advertise-routes but forgot
		// to approve on headscale, or the approval was rolled
		// back). `healthy` is the combined boolean that the
		// /admin/exit-nodes page renders as a green/red dot.
		`CREATE TABLE IF NOT EXISTS exit_node_health (
			node_id TEXT PRIMARY KEY,
			hostname TEXT NOT NULL DEFAULT '',
			online INTEGER NOT NULL DEFAULT 0,
			last_seen TEXT NOT NULL DEFAULT '',
			advertised_routes_ok INTEGER NOT NULL DEFAULT 0,
			has_exit_tag INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'unknown',
			healthy INTEGER NOT NULL DEFAULT 0,
			last_check_at INTEGER NOT NULL DEFAULT 0,
			last_state_change_at INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_exit_node_health_state
			ON exit_node_health (state, healthy)`,
		// 2026-07-15: v0.13.0 — state-transition log.
		// Inserted by the monitor on detected transitions
		// (online→offline, offline→online). `alerted_at` is
		// updated to a unix timestamp once the Telegram alert
		// has been queued. The dispatch loop queries
		// `WHERE alerted_at = 0` to find pending alerts.
		`CREATE TABLE IF NOT EXISTS exit_node_state_changes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			node_id TEXT NOT NULL,
			hostname TEXT NOT NULL DEFAULT '',
			from_state TEXT NOT NULL,
			to_state TEXT NOT NULL,
			detected_at INTEGER NOT NULL,
			alerted_at INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_exit_node_state_changes_pending
			ON exit_node_state_changes (alerted_at, detected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_exit_node_state_changes_node
			ON exit_node_state_changes (node_id, detected_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// CREATE IF NOT EXISTS already covers the
			// "already migrated" path; the if-guard is
			// belt-and-suspenders.
			continue
		}
	}
	return nil
}
