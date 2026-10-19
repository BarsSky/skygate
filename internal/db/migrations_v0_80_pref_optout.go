// migrations_v0_80_pref_optout.go — v0.80 (B399): the operator's "no preference"
// must be a decision the engine can SEE, not an absence it re-fills.
//
// WHY (measured on the operator's installation 2026-10-19, verbatim):
//
//	«теперь несмотря на то что для skyworker я отключаю strict mode по karolina он
//	 ставиться обратно»
//
// The audit log says exactly what happened, three times in six hours:
//
//	19:08:56  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
//	18:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
//	          tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=269
//	17:30:39  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
//	13:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
//	          tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=224
//
// `SetDeviceExitNodePref(…, "")` DELETED the row, so the B229 preferred-exit
// reconciler saw a device with rules and no preference and — doing exactly what it
// was built to do — created one again, with `via_enabled = 1`. The operator could
// not un-pin a device by hand at all: the next tick restored the pin. And because
// `via` on an `autogroup:internet` grant is a FILTER (B398), restoring the pin is
// restoring the restriction.
//
// V080 adds the missing bit of memory:
//
//	opted_out — 1 when a HUMAN removed this device's preference (or cleared its
//	            strict pin). The reconciler must never create or rewrite such a
//	            row; only an explicit action from the panel clears the flag again.
//	            0 (the default) for every derived row and for every fresh install,
//	            so existing behaviour is unchanged where nobody said "no".
//
// The row is KEPT (with an empty exit_node_tag) instead of deleted: deletion is
// precisely what erased the human's decision. Kept + flagged, it is both the
// opt-out marker and the record of who removed it.
package db

import (
	"database/sql"
	"fmt"
)

const prefOptOutColumnSQL = `ALTER TABLE device_exit_node_prefs ADD COLUMN IF NOT EXISTS opted_out INTEGER NOT NULL DEFAULT 0`

// migrateV080PG adds device_exit_node_prefs.opted_out on PostgreSQL.
func migrateV080PG(d *sql.DB) error {
	if _, err := d.Exec(prefOptOutColumnSQL); err != nil {
		return fmt.Errorf("v0.80 device_exit_node_prefs.opted_out: %w", err)
	}
	return nil
}

// migrateV080SQLite adds device_exit_node_prefs.opted_out on SQLite through the
// single DDL chokepoint (SQLite has no ADD COLUMN IF NOT EXISTS).
func migrateV080SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{prefOptOutColumnSQL}); err != nil {
		return fmt.Errorf("v0.80 device_exit_node_prefs.opted_out (sqlite): %w", err)
	}
	return nil
}
