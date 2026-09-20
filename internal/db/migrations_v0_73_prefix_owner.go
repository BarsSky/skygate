// migrations_v0_73_prefix_owner.go — v0.73 (B275): the prefix→relay
// assignment table.
//
// WHY (2026-09-20): headscale serves a subnet prefix from exactly ONE
// relay (the primary), so "device A reaches X via karolina and device B
// reaches X via emilia" is not expressible — the previous model kept
// `exit_node_id` on every rule and let each relay advertise its own
// rules, which collided (28 Cloudflare/Google ranges claimed by both
// `basic`→emilia and `skyworker`→karolina) and made the primary flap.
//
// B275 moves the decision to skygate: every prefix gets ONE owner in
// this table, computed by internal/prefixowner (explicit rule wins;
// otherwise least-loaded relay with sticky re-use of the previous
// owner), the relay advertises exactly its owned set, and the per-CIDR
// ACL grant carries `via=[owner]` so the device follows the relay that
// can actually serve it.
//
//	prefix        — the CIDR, e.g. '104.16.0.0/12'
//	exit_node_id  — owning relay hostname (as in device_rules.exit_node_id)
//	source        — 'explicit' (rules named it), 'manual' (operator
//	                pinned it — never overwritten by the engine), 'auto'
//	                (engine choice, may move when the owner is unhealthy)
//	claims        — how many enabled rules asked for this prefix
//	devices       — how many distinct devices asked for it
//	updated_at    — unix seconds of the last assignment write
//
// Both chains create the same shape; the SQLite side goes through
// execSQLiteDDL like every other schema change.
package db

import (
	"database/sql"
	"fmt"
)

const prefixOwnerTableSQL = `
CREATE TABLE IF NOT EXISTS prefix_owner (
	prefix       TEXT PRIMARY KEY,
	exit_node_id TEXT NOT NULL,
	source       TEXT NOT NULL DEFAULT 'auto',
	claims       INTEGER NOT NULL DEFAULT 0,
	devices      INTEGER NOT NULL DEFAULT 0,
	updated_at   INTEGER NOT NULL DEFAULT 0
)`

const prefixOwnerExitIdxSQL = `CREATE INDEX IF NOT EXISTS idx_prefix_owner_exit ON prefix_owner(exit_node_id)`

// migrateV073PG creates prefix_owner on PostgreSQL.
func migrateV073PG(d *sql.DB) error {
	if _, err := d.Exec(prefixOwnerTableSQL); err != nil {
		return fmt.Errorf("v0.73 prefix_owner: %w", err)
	}
	if _, err := d.Exec(prefixOwnerExitIdxSQL); err != nil {
		return fmt.Errorf("v0.73 prefix_owner idx: %w", err)
	}
	return nil
}

// migrateV073SQLite creates prefix_owner on SQLite through the single
// DDL chokepoint.
func migrateV073SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{prefixOwnerTableSQL, prefixOwnerExitIdxSQL}); err != nil {
		return fmt.Errorf("v0.73 prefix_owner (sqlite): %w", err)
	}
	return nil
}
