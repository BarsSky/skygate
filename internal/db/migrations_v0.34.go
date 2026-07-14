package db

import "database/sql"

// migrateV034 (2026-07-14): hostname column on node_owner_map.
//
// Этап 14 v10 — /my_nodes and /admin/nodes in the bot now show
// "hostname (node_id) [tag]" so users recognise their devices
// by the friendly name (set via `tailscale up --hostname=...`)
// rather than the Tailscale node_id. The hostname is sourced
// from headscale on each backfill and stored in this column;
// existing rows are NOT backfilled in this migration (that's
// done lazily by the next backfillNodeOwnership run, which
// iterates every node and pulls the headscale view).
//
// Why a column and not a live headscale call: the bot's
// /my_nodes runs on every /my_nodes invocation; calling
// headscale.ListNodes on each one would be N HTTP round-trips
// per user message. Caching the hostname in node_owner_map is
// a single SELECT and survives across restarts. The
// backfill is the only place that writes it, so the
// staleness window is bounded by the next backfill
// (operator-driven via /admin/devices "Backfill").
//
// Empty string is the "not yet looked up" sentinel — the bot
// falls back to the raw node_id when hostname is empty, so
// the migration is safe even before backfill runs.
func migrateV034(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE node_owner_map ADD COLUMN hostname TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_node_owner_map_hostname ON node_owner_map (hostname) WHERE hostname != ''`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// Column / index already exists — that's the
			// "already migrated" path. Continue.
			continue
		}
	}
	return nil
}
