// migrations_v0_74_all_devices.go — v0.74 (B276.1): "all my devices" as a live
// intent, not a one-off fan-out.
//
// WHY (2026-09-21): /my/exit-rules has offered "all my devices" since B275.3, but
// the form only MATERIALISED the rule for the devices the user owned at that
// moment: `PostMyExitRule` inserts one row per device and forgets why. A device
// registered a week later has no rule — the user sees a per-user intent in the UI
// ("это правило для всех моих устройств") that silently does not cover their new
// laptop, and nothing anywhere says so.
//
// V074 stores the intent on the row, so a propagation pass can re-materialise it
// for the user's current device set on every tick:
//
//	all_devices  — 1 when the rule was saved for "all my devices" of its owner.
//	               0 (the default) for a rule aimed at one specific device, which
//	               must NEVER be copied to another one.
//
// The column is additive with a default, so an existing installation keeps its
// behaviour: rows created before V074 are single-device rules (that is what they
// were — the fan-out copies are indistinguishable from hand-made ones, and
// guessing "this looks like it was for all devices" would silently start copying
// rules the user never asked to copy).
//
// The partial index keeps the propagation query cheap: it only ever looks at
// `all_devices = 1`, which is a small fraction of device_rules.
package db

import (
	"database/sql"
	"fmt"
)

const allDevicesColumnSQL = `ALTER TABLE device_rules ADD COLUMN IF NOT EXISTS all_devices INTEGER NOT NULL DEFAULT 0`

const allDevicesIndexSQL = `CREATE INDEX IF NOT EXISTS idx_device_rules_all_devices ON device_rules(all_devices) WHERE all_devices = 1`

// migrateV074PG adds device_rules.all_devices on PostgreSQL.
func migrateV074PG(d *sql.DB) error {
	if _, err := d.Exec(allDevicesColumnSQL); err != nil {
		return fmt.Errorf("v0.74 device_rules.all_devices: %w", err)
	}
	if _, err := d.Exec(allDevicesIndexSQL); err != nil {
		return fmt.Errorf("v0.74 device_rules.all_devices idx: %w", err)
	}
	return nil
}

// migrateV074SQLite adds device_rules.all_devices on SQLite through the single
// DDL chokepoint (SQLite has no ADD COLUMN IF NOT EXISTS; execSQLiteDDL routes
// ADD COLUMN through addColumnIfMissingSQLite).
func migrateV074SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{allDevicesColumnSQL, allDevicesIndexSQL}); err != nil {
		return fmt.Errorf("v0.74 device_rules.all_devices (sqlite): %w", err)
	}
	return nil
}
