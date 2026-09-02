// v1.5.0+ (B211) — UNIQUE constraint on cluster_node.
//
// Phase 2.3 of docs/internal/cluster-management.md
// (`skygate init` idempotent bootstrap). The new
// `skygate init` CLI subcommand needs an ON CONFLICT
// (cluster_id, hostname) target so it can refresh
// tailscale_ip / skygate_version / roles on a re-run
// without creating duplicate rows.
//
// Without this constraint:
//   - `skygate init` re-run would either fail (with a
//     SQL syntax error on the ON CONFLICT clause) or
//     silently create a second row for the same node
//   - AddNode / Join would also be vulnerable to
//     duplicate inserts (the AddNode comment explicitly
//     says "we'll add a UNIQUE constraint in a follow-up
//     migration" — B195.1 was the planned name; this
//     migration is that follow-up)
//
// With the constraint:
//   - The UpsertNode helper (B211, internal/cluster/node.go)
//     works correctly on a fresh DB and on a re-run
//   - The constraint is the canonical "node id" guard
//     (cluster_id + hostname) is the natural key
//
// Idempotent: ADD CONSTRAINT IF NOT EXISTS in a
// DO block — safe to re-run on a DB that already has it.

package db

import (
	"database/sql"
)

// migrateV066PG — v1.5.0+ (B211) —
//   UNIQUE (cluster_id, hostname) on cluster_node.
//
// PG has no "ADD CONSTRAINT IF NOT EXISTS" before v17;
// we wrap the ADD in a DO block that checks
// pg_constraint first. Same pattern as the existing
// migrations that add indexes via CREATE INDEX IF NOT
// EXISTS (which is supported in all current PG versions).
//
// The check is:
//
//   SELECT 1 FROM pg_constraint
//    WHERE conrelid = 'cluster_node'::regclass
//      AND contype  = 'u'
//      AND conname  = 'cluster_node_cluster_id_hostname_key'
//
// If the constraint is missing, ADD it. We pick the
// "_key" suffix to match PG's default naming for
// UNIQUE constraints derived from a column-list UNIQUE
// clause (so `psql \d cluster_node` shows the same name
// whether the constraint was added by this migration
// or by a manual UNIQUE clause in the original CREATE
// TABLE).
func migrateV066PG(d *sql.DB) error {
	stmts := []string{
		// 1. The UNIQUE constraint itself, idempotent.
		`DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'cluster_node'::regclass
       AND contype  = 'u'
       AND conname  = 'cluster_node_cluster_id_hostname_key'
  ) THEN
    ALTER TABLE cluster_node
      ADD CONSTRAINT cluster_node_cluster_id_hostname_key
      UNIQUE (cluster_id, hostname);
  END IF;
END$$`,
		// 2. The matching index (PG creates one
		//    automatically for the UNIQUE constraint
		//    above, but we add a separate CREATE
		//    IF NOT EXISTS so a DB that was created
		//    with a column-list UNIQUE in the CREATE
		//    TABLE (B195) and now has both an
		//    automatically-named index AND this
		//    constraint won't see a duplicate-index
		//    error). The IF NOT EXISTS is a no-op
		//    when PG already has the constraint's
		//    backing index.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cluster_node_cluster_hostname
		   ON cluster_node (cluster_id, hostname)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
