// migrations_v0_77_exit_location.go — v0.77 (B312): WHERE each exit node actually
// sits, so the operator can see it and the assignment engine can prefer a nearby
// relay.
//
// WHY (2026-09-23): the operator asked for two things in one sentence — show the
// location of every exit node, and when one becomes unreachable, compensate on the
// OTHERS with a priority by location ("приоритет расставлять на сходный признак
// расположения"). Both need the same missing fact: nothing in the schema said where
// a relay is. The live case behind it: `karolina` became unreachable (a provider
// block) and its 75 prefixes moved to whatever relay answered first — correct for
// reachability, arbitrary for latency, because the engine had no idea that one relay
// sits in the same country as karolina and the other does not.
//
// V077 adds five columns to exit_servers:
//
//	location_label      — what the operator sees ("DE · Frankfurt am Main")
//	location_country    — ISO-ish country name/code for the same-country tier
//	location_lat        — latitude  (0 when unknown; lat 0 is a valid coordinate, so
//	location_lon          "unknown" is expressed by location_checked_at = 0)
//	location_source     — manual | auto  (a manual value is never overwritten)
//	location_checked_at — unix time of the last auto-detect attempt (0 = never)
//
// All five are additive with defaults, so an existing installation keeps working and
// every relay simply starts with "location unknown" — no backfill can invent a
// location, and guessing one would be worse than showing nothing (the assignment
// would silently prefer a relay for a reason the operator cannot see).
//
// The index on `location_checked_at` keeps the periodic auto-detect cheap: it looks
// only at rows that are due (never checked, or checked longer ago than the refresh
// window).
package db

import (
	"database/sql"
	"fmt"
)

// exitLocationColumnSQLs adds the location columns (idempotent on both dialects;
// SQLite goes through execSQLiteDDL → addColumnIfMissingSQLite).
var exitLocationColumnSQLs = []string{
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_label TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_country TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_lat TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_lon TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_source TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS location_checked_at BIGINT NOT NULL DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS idx_exit_servers_location_checked ON exit_servers(location_checked_at)`,
}

// migrateV077PG adds the exit_servers location columns on PostgreSQL.
func migrateV077PG(d *sql.DB) error {
	for _, q := range exitLocationColumnSQLs {
		if _, err := d.Exec(q); err != nil {
			return fmt.Errorf("v0.77 exit_servers location (%s): %w", q, err)
		}
	}
	return nil
}

// migrateV077SQLite adds the same columns through the single DDL chokepoint
// (SQLite has no ADD COLUMN IF NOT EXISTS).
func migrateV077SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, exitLocationColumnSQLs); err != nil {
		return fmt.Errorf("v0.77 exit_servers location (sqlite): %w", err)
	}
	return nil
}
