package db

import "database/sql"

// migrateV052 (v0.33.1.19, 2026-08-09): fix via_enabled column
// corruption in user_exit_node_prefs and device_exit_node_prefs.
//
// Background. SetUserExitNodePref (in migrations_v0.45.go) and
// SetDeviceExitNodePref (in migrations_v0.46.go) had a
// positional-mismatch bug in their INSERT clause: the VALUES
// list put viaInt (a 0/1 bool) in the position mapped to
// updated_at, and nowUnixSQL() (a unix timestamp > 1.7e9) in
// the position mapped to via_enabled. Every row written by
// those functions between v0.28.5 (when via_enabled was added)
// and v0.33.1.19 (when this migration runs) has the two columns
// swapped.
//
// Net effect on production:
//   - UI: /my/exit-nodes always shows the "strict" badge
//     because via_enabled = nowUnixSQL != 0 is always true.
//     The checkbox to disable strict mode is a no-op —
//     un-checking writes a new row with the same swap
//     (via_enabled=new_timestamp, which is still truthy).
//   - ACL: GenerateACLWithViaForPlane's per-user grant loop
//     (acl.go:802) tests `ep.ViaEnabled` to decide whether
//     to emit `via: [tag:exit-...]` in the per-user grant.
//     With via_enabled=nowUnixSQL (always truthy), every
//     per-user grant has via, which Tailscale interprets as
//     "pin this user to the preferred exit-node". The
//     operator's "advisory" mode (via=0) never took effect.
//   - Per-device grants: same swap. Per-device via_enabled
//     is always truthy, so the per-device exit-node pref
//     always emits via=tag:exit-<hostname>. Per-device
//     "advisory" toggles on /my/devices are no-ops.
//
// This migration is the data-repair half of the v0.33.1.19
// fix. It walks both tables and, for every row where the
// current "via_enabled" value is a unix timestamp
// (> some threshold set conservatively at 1_000_000_000 =
// Sep 2001 — anything below that is the legitimate 0/1
// value) AND the current "updated_at" value is a small
// integer (0 or 1, the mis-swapped viaInt), swap the two
// columns. Rows that already look correct (via_enabled in
// {0, 1} and updated_at is a unix timestamp) are left
// alone — that means the row was already repaired
// manually, or was inserted by a future-correct version
// of the code.
//
// Why the threshold. A legitimate via_enabled is 0 (advisory
// mode, default) or 1 (strict mode). A unix timestamp for
// 2026 is around 1.7e9. 1e9 = 2001-09-09, well before skygate
// existed. The threshold of 1_000_000_000 is a safe
// discriminator: only swap when the values are clearly
// transposed (updated_at in {0, 1}, via_enabled > 1e9).
//
// Idempotency. The WHERE clause filters to only the corrupt
// rows. Running this migration twice is a no-op (the second
// run finds nothing to swap). A row that was already swapped
// to the correct layout has via_enabled in {0, 1} and
// updated_at > 1e9, so the WHERE clause skips it.
//
// The SQL uses a temp-stored updated_at variable because
// SQLite+PG don't have a native swap. We do the swap by
// writing the current via_enabled into updated_at and the
// current updated_at into via_enabled — but only when the
// discriminant above says it's safe.
//
// On the code side, SetUserExitNodePref and
// SetDeviceExitNodePref were also fixed in v0.33.1.19 to
// write the columns in the right order. New rows after
// v0.33.1.19 are correct from the start.
func migrateV052(d *sql.DB) error {
	// 2026-08-09: the threshold 1_000_000_000 is 2001-09-09
	// in unix time — any via_enabled above that is a
	// updated_at, when mis-swapped, holds viaInt (0 or 1).
	// The where clause is the inverse of the threshold:
	// updated_at in {0, 1} AND via_enabled > threshold.
	// Both predicates must hold for the swap to be
	// safe — if either is off, the row is already correct
	// (or holds values we can't safely distinguish from
	// a legitimate row, in which case we leave it alone).
	stmts := []string{
		`UPDATE user_exit_node_prefs
		    SET updated_at = via_enabled,
		        via_enabled = updated_at
		  WHERE updated_at IN (0, 1)
		    AND via_enabled > 1000000000`,
		`UPDATE device_exit_node_prefs
		    SET updated_at = via_enabled,
		        via_enabled = updated_at
		  WHERE updated_at IN (0, 1)
		    AND via_enabled > 1000000000`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
