// 2026-09-03: v0.68 (B232) — repair device_rules_natural_key_uniq
// shape drift caused by V056 idempotency gap.
//
// What this fixes
// ---------------
// V056 (B188.2 era, deploy ~2026-08-17) was the migration
// that defined the natural-key UNIQUE INDEX on
// device_rules. V056's CREATE statement includes
// `parent_domain`:
//
//   CREATE UNIQUE INDEX IF NOT EXISTS device_rules_natural_key_uniq
//     ON device_rules(user_id, device_id, exit_node_id,
//                     target_type, target_value, parent_domain)
//
// But `CREATE UNIQUE INDEX IF NOT EXISTS` is a no-op when an
// index with the same NAME already exists, even if the
// column list differs. Pre-V056, the natural_key_uniq
// was a 5-column index (no `parent_domain`) defined by an
// earlier migration (the original `qSelectRuleByComposite`
// contract from v0.55). So on every DB that was upgraded
// past V055 before V056, the V056 statement was a silent
// no-op and the index STAYED 5-col.
//
// Then B188.2 changed `qInsertDeviceRule` to use a 6-col
// ON CONFLICT clause (added `parent_domain` to the natural
// key to dedup domain-resolved /32 rules per parent). The
// INSERT with 6-col ON CONFLICT now needs a 6-col UNIQUE
// INDEX, but the live DB only has 5-col. Every INSERT
// of a new rule failed with:
//
//   ERROR: there is no unique or exclusion constraint
//          matching the ON CONFLICT specification
//
// (pre-B188.2 INSERTs with a 5-col ON CONFLICT would have
// been fine; the B188.2 ON CONFLICT was the trigger that
// exposed the gap.)
//
// Symptom observed in production (2026-09-03): /my/exit-rules
// POST returned "db error" for every new rule; the live
// fix (one-time DROP + CREATE) restored normal operation.
//
// This migration makes the fix permanent: it always
// re-creates `device_rules_natural_key_uniq` with the
// 6-col definition, regardless of what shape the index
// already has.
//
// Safety
// ------
// 1. Pre-flight check: SELECT any 6-tuple duplicates
//    before DROP. If duplicates exist, refuse to run
//    and tell the operator to run scripts/check_b125.sh
//    + a manual cleanup first. (V056's audit found
//    0 duplicates on the live DB; the check is here
//    for future deploys.)
// 2. DROP IF EXISTS — safe even if the index doesn't exist.
// 3. CREATE UNIQUE INDEX — fails clearly if the table
//    has 6-tuple duplicates (caught by the pre-flight).
// 4. ANALYZE device_rules — refresh the planner's
//    statistics so the new index is picked up.
//
// Idempotency: applied_migrations table records v68
// after success. Re-running the migration is a no-op
// (the index is already 6-col; CREATE UNIQUE INDEX
// would fail on a duplicate; but the pre-flight check
// makes this safe — duplicates on 6-tuple would be
// caught first and the migration would refuse to run).
package db

import (
	"database/sql"
	"fmt"
)

// migrateV068PG is the B232 migration. Run after
// migrateV067PG (B221, audit_log.target_type + target_id).
//
// 2026-09-03: v0.68 (B232) — repair device_rules_natural_key_uniq
func migrateV068PG(d *sql.DB) error {
	// Step 1: pre-flight duplicate check on the 6-tuple
	// (the V056 contract). If any (user, device, exit,
	// type, value, parent) appears more than once, refuse
	// to DROP the index — the CREATE would fail anyway,
	// and the operator should clean up duplicates first.
	var dupCount int
	if err := d.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT 1
			  FROM device_rules
			 GROUP BY user_id, device_id, exit_node_id,
			          target_type, target_value, parent_domain
			HAVING COUNT(*) > 1
			 LIMIT 1
		) AS dup
	`).Scan(&dupCount); err != nil {
		return fmt.Errorf("v0.68 (B232) pre-flight duplicate check: %w", err)
	}
	if dupCount > 0 {
		return fmt.Errorf("v0.68 (B232) pre-flight FAILED: device_rules has 6-tuple duplicates. Run scripts/check_b125.sh for the cleanup audit; deduplicate before re-running migrateV068PG")
	}

	// Step 2: DROP + recreate. CREATE UNIQUE INDEX IF NOT
	// EXISTS is a no-op when the index name exists with
	// a different shape, so we must DROP first. The
	// pre-flight check above ensures the CREATE will
	// succeed (no duplicates on the 6-tuple).
	stmts := []string{
		`DROP INDEX IF EXISTS device_rules_natural_key_uniq`,
		`CREATE UNIQUE INDEX device_rules_natural_key_uniq
			ON device_rules(user_id, device_id, exit_node_id,
			                target_type, target_value, parent_domain)`,
		`ANALYZE device_rules`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return fmt.Errorf("v0.68 (B232) stmt %q: %w", s[:60], err)
		}
	}
	return nil
}
