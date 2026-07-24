// v0.28.0 — per-device ACL rules via tag:dev-<user>-<device>.
//
// The pre-v0.28.0 GenerateACL rendered per-device exit-rules
// with src = device_ip (Tailscale IP at the time the rule
// was created). That works in the simple case but has two
// sharp edges:
//
//   1. The Tailscale IP can change on reconnection (the
//      client re-registers and headscale hands out a new
//      IP from the 100.64.0.0/10 pool). When that happens,
//      the rule with src=100.64.0.1 silently stops
//      applying. The next ACL re-apply reads device_ip
//      from device_rules (which still has the OLD IP),
//      so the rule points at an address the device no
//      longer owns.
//
//   2. The rule src is a hardcoded IP, so any device
//      that happens to acquire that IP inherits the
//      rule. Tailscale IPs are assigned by headscale and
//      are not predictable across re-registers, but the
//      failure mode is the same: a rule that "belongs"
//      to skyworker can land on msi if headscale hands
//      msi the IP that was skyworker's yesterday.
//
// The v0.28.0 fix: tag each device with a unique
// per-user-per-device tag at register time, and use
// the tag as src in the ACL:
//
//   src = "tag:dev-<user>-<device>"
//
// The tag is owned by the user (per-user tagOwners
// entry), the device is auto-tagged on every /my/devices
// load (v0.28.0 backfillNodeOwnership change), and the
// tag survives IP changes.
//
// Schema changes:
//
//   device_rules.user_name        TEXT NOT NULL DEFAULT ''
//   device_rules.device_hostname  TEXT NOT NULL DEFAULT ''
//
// The new columns are populated at rule INSERT time
// (AppendDeviceRule, bot + admin forms) and at migration
// time for any pre-existing rows. The backfill uses:
//
//   - portal_users.username           → device_rules.user_name
//   - node_owner_map.hostname (best-effort via device_ip
//     JOIN)                          → device_rules.device_hostname
//
// Rules where the backfill can't find a matching node
// (device offline, headscale hiccup, etc.) keep
// user_name="" / device_hostname="" and the ACL builder
// falls back to src=device_ip — the same behavior the
// pre-v0.28.0 path had, so nothing regresses.

package db

import (
	"database/sql"
	"fmt"
)

func migrateV044(d *sql.DB) error {
	if _, err := d.Exec(`ALTER TABLE device_rules ADD COLUMN user_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("v0.44 add user_name: %w", err)
	}
	if _, err := d.Exec(`ALTER TABLE device_rules ADD COLUMN device_hostname TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("v0.44 add device_hostname: %w", err)
	}
	// Backfill user_name from portal_users. Every
	// device_rules row has a non-zero user_id (the FK
	// is NOT NULL); the portal_users row might be gone
	// in pathological cleanup cases, in which case we
	// leave user_name empty and the ACL builder falls
	// back to device_ip.
	if _, err := d.Exec(`
		UPDATE device_rules
		   SET user_name = COALESCE(
		     (SELECT username FROM portal_users WHERE id = device_rules.user_id),
		     ''
		   )
		 WHERE user_name = ''`); err != nil {
		return fmt.Errorf("v0.44 backfill user_name: %w", err)
	}
	// device_hostname is NOT backfilled here — node_owner_map
	// has no tailscale_ip column to JOIN on. The runtime
	// backfill happens in handlers.backfillNodeOwnership
	// on the next /my/devices load (the function knows the
	// device hostname from headscale's list response).
	// For pre-v0.28.0 rules, the ACL builder falls back to
	// src=device_ip until the runtime backfill lands, so
	// the rules are live immediately and switch to the
	// tag-based src transparently on the next /my/devices
	// load.
	return nil
}
