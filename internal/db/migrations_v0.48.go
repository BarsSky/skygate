// v0.31.x — per-device OS + device_type metadata for
// easier debugging.
//
// Background (2026-07-29, operator report):
// "при тесте пользователем michail доступ своего устройства
// base к подключению по exit node не дает никакого доступа ...
// предлагаю вести еще пометку по устройством к какому типу оно
// относится и какая операционная система проще будет отлаживать
// и рассматривать варианты так base - windows nothing-phone-2 -
// android".
//
// The operator wants a per-device OS + type marker so the
// /my/devices and /admin/devices pages surface the platform
// alongside the hostname. This way "is this an Android phone
// or a Windows box?" is one glance, not a /exit_nodes audit.
//
// Schema:
//   node_owner_map.os           TEXT NOT NULL DEFAULT 'unknown'
//   node_owner_map.device_type  TEXT NOT NULL DEFAULT 'unknown'
//
// We don't add an explicit "manual override" column — the
// rule is: if the stored value is "unknown" (the default),
// the auto-detect overwrites it on the next /my/devices load.
// If the stored value is anything else, the auto-detect
// skips the row (the operator set it explicitly). The admin
// can re-set to "unknown" via /admin/devices/{id}/meta to
// re-enable the auto-detect.
//
// The auto-detect lives in internal/devicemeta/ (new
// package). It's called from the /my/devices load path
// (which already has the backfillNodeOwnership hook), so
// every user's first /my/devices visit auto-populates
// their device metadata without an admin action.

package db

import "database/sql"

// addDeviceMetaNodeOwnerMapOSSQL is the first ALTER for
// v0.48. Adds the OS column with default 'unknown'.
// Idempotent: the duplicate-column error on a re-run is
// caught and silently ignored (matching the pattern in
// migrateV047).
//
// 2026-07-29.
const addDeviceMetaNodeOwnerMapOSSQL = `ALTER TABLE node_owner_map ADD COLUMN os TEXT NOT NULL DEFAULT 'unknown'`

// addDeviceMetaNodeOwnerMapTypeSQL adds the device_type
// column. Same idempotency pattern.
const addDeviceMetaNodeOwnerMapTypeSQL = `ALTER TABLE node_owner_map ADD COLUMN device_type TEXT NOT NULL DEFAULT 'unknown'`

func migrateV048(d *sql.DB) error {
	// v0.48 has 2 ALTERs. We run them idempotently
	// (silently skip "duplicate column" errors so a
	// restart on a deploy that already ran v0.48 is
	// a no-op). See migrateV047 for the pattern.
	if _, err := d.Exec(addDeviceMetaNodeOwnerMapOSSQL); err != nil {
		if !isSQLiteDuplicateColumnError(err) {
			return err
		}
	}
	if _, err := d.Exec(addDeviceMetaNodeOwnerMapTypeSQL); err != nil {
		if !isSQLiteDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}
