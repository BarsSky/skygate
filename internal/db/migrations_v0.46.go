// v0.28.4 — per-device preferred exit-node.
//
// 2026-07-25: v0.28.1 added user_exit_node_prefs (one row per
// user) so the operator can pin a user to a specific exit-node.
// v0.28.3 closed the catch-all bypass by making
// `tag:public → autogroup:internet` the only catch-all for
// autogroup:internet — every user can reach the internet
// through their own grant, with via=[<preferred>]. That
// breaks the operator's workstation-3 → relay-3 setup (workstation-3 is
// tag:dev-admin-workstation-3 → admin@..., and admin's
// per-user via is relay-1, so workstation-3 is pinned to relay-1).
//
// v0.28.4 is the per-device override. The data model is a
// new table `device_exit_node_prefs` keyed on
// (user_id, device_hostname). When a device has a per-device
// pref, GenerateACLWithViaForPlane emits a per-device grant
// BEFORE the per-user grant (Tailscale first-match wins) with
// src=tag:dev-<user>-<device> and via=[<device-pref>].
//
// Why a separate table (not a column on user_exit_node_prefs):
//   * user_exit_node_prefs has user_id PK, 1 row per user.
//     Extending it to (user_id, device_hostname) PK breaks
//     the v0.28.1 SetUserExitNodePref contract (the helper
//     treats user_id as the unique key).
//   * Per-device pref is logically a different entity:
//     "user's default" vs "this specific device's override".
//     A user may have a default (relay-1) and a single device
//     override (workstation-3 → relay-3). Both rows can coexist.
//   * Per-device prefs are set by the user on /my/devices
//     (their own devices) or by the admin on /admin/devices
//     (any device). The set_by_user_id column tracks which
//     path set the row.
//
// The hostname is the lowercase form (matching the v0.28.0
// backfill convention: tag:dev-<user>-<device> where <device>
// is the lowercased hostname with non-alphanumeric characters
// replaced). The UI preserves the original case for display
// but the pref key is normalized to lowercase.

package db

import (
	"database/sql"
)

// DeviceExitNodePref is one row of device_exit_node_prefs.
// One row per (user_id, device_hostname) — a specific
// device's exit-node override. To change the preference,
// the user / admin UPDATEs the row. To remove the
// override, the row is deleted (so the device falls back
// to the user's per-user pref, or no via at all if the
// user has no pref).
//
// 2026-07-25: v0.28.4.
// 2026-07-25: v0.28.5 — added ViaEnabled. When true, the
// ACL builder emits a per-device grant with via=[].
// When false, the per-device grant is SKIPPED entirely
// (the device falls back to the per-user grant's via,
// or no via if the user has no via_enabled pref).
// This opt-in is necessary for older Tailscale clients
// (Android) that reject policies with via they don't
// understand.
type DeviceExitNodePref struct {
	UserID          int64
	Username        string
	DeviceHostname  string
	ExitNodeTag     string
	UpdatedAt       int64
	SetByUserID     int64
	ViaEnabled      bool
}

// GetDeviceExitNodePref returns the device's preferred
// exit-node tag, or "" if the device has no per-device
// pref set. Joins portal_users for the username so the
// template can render "<user>-<device>'s preferred
// exit-node: <tag>" without an extra round-trip.
//
// 2026-07-25: v0.28.4.
// 2026-07-25: v0.28.5 — also returns ViaEnabled.
func GetDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname string) (DeviceExitNodePref, error) {
	var p DeviceExitNodePref
	var viaEnabled int
	// v0.33.1.14 fix: was placeholdersList(1)+placeholdersList(1)
	// which on PG produced "$1 AND p.device_hostname = $1" (two
	// refs to the same param). Use PlaceholderAt(2, i) which
	// internally calls PlaceholdersList(2) and indexes into it,
	// so PG gets "$1 AND p.device_hostname = $2" (the correct,
	// unique placeholders).
	err := d.QueryRow(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = `+PlaceholderAt(2, 0)+` AND p.device_hostname = `+PlaceholderAt(2, 1),
		userID, deviceHostname,
	).Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err == nil {
		p.ViaEnabled = viaEnabled != 0
	}
	return p, err
}

// SetDeviceExitNodePref upserts the device's preferred
// exit-node. Pass exitNodeTag = "" to clear the preference
// (the row is deleted, not set to empty — keeps the table
// small and makes "has per-device pref?" a simple
// SELECT EXISTS check).
//
// setByUserID is recorded in the row so the admin can see
// "alice set this for her own device" vs. "admin set
// this for alice's device" on /admin/devices.
//
// 2026-07-25: v0.28.5 — added viaEnabled param. When true,
// the ACL builder emits a per-device grant with via=[].
// When false (the new default for fresh rows), the
// per-device grant is SKIPPED — the device falls back
// to the per-user grant's via (or no via if the user has
// no via_enabled pref). The "preferred exit-node" is still
// stored and shown in the UI; it's just not enforced.
func SetDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname, exitNodeTag string, setByUserID int64, viaEnabled bool) error {
	if exitNodeTag == "" {
		// v0.33.1.14 fix: was placeholdersList(1)+placeholdersList(1)
		// which on PG produced "$1 AND device_hostname = $1" (two
		// refs to the same param). Same PlaceholderAt(2, i) fix
		// as GetDeviceExitNodePref above.
		_, err := d.Exec(`DELETE FROM device_exit_node_prefs WHERE user_id = `+PlaceholderAt(2, 0)+` AND device_hostname = `+PlaceholderAt(2, 1),
			userID, deviceHostname)
		return err
	}
	viaInt := 0
	if viaEnabled {
		viaInt = 1
	}
	_, err := d.Exec(`
		INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (`+placeholdersList(5)+`, `+nowUnixSQL()+`)
		ON CONFLICT(user_id, device_hostname) DO UPDATE SET
			exit_node_tag = excluded.exit_node_tag,
			set_by_user_id = excluded.set_by_user_id,
			updated_at = excluded.updated_at,
			via_enabled = excluded.via_enabled`,
		userID, deviceHostname, exitNodeTag, setByUserID, viaInt)
	return err
}

// ListAllDeviceExitNodePrefs returns every row, joined
// with portal_users for the username. Used by
// GenerateACLWithViaForPlane to build the per-device
// `via` set in one pass. The result is small (single-
// digit rows in production; only devices the user has
// explicitly pinned) so we don't worry about pagination.
//
// 2026-07-25: v0.28.5 — also returns ViaEnabled. The ACL
// builder uses this to decide whether to emit the
// per-device grant (via_enabled=true) or skip it
// (via_enabled=false — the device has a "preferred"
// exit-node for the UI but no policy-level pinning).
func ListAllDeviceExitNodePrefs(d *sql.DB) ([]DeviceExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 ORDER BY pu.username, p.device_hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceExitNodePref
	for rows.Next() {
		var p DeviceExitNodePref
		var viaEnabled int
		if err := rows.Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled); err != nil {
			return nil, err
		}
		p.ViaEnabled = viaEnabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListDeviceExitNodePrefsForUser returns the prefs for one
// user (so the /my/devices page can render the dropdown
// without an extra JOIN). Returns nil (not an error) when
// the user has no per-device prefs.
//
// 2026-07-25: v0.28.4.
// 2026-07-25: v0.28.5 — also returns ViaEnabled.
func ListDeviceExitNodePrefsForUser(d *sql.DB, userID int64) ([]DeviceExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = `+placeholdersList(1)+`
		 ORDER BY p.device_hostname`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceExitNodePref
	for rows.Next() {
		var p DeviceExitNodePref
		var viaEnabled int
		if err := rows.Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled); err != nil {
			return nil, err
		}
		p.ViaEnabled = viaEnabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func migrateV046(d *sql.DB) error {
	// 2026-07-25: v0.28.4 — device_exit_node_prefs table.
	// One row per (user_id, device_hostname). Composite
	// primary key enforces uniqueness. Cascade on user
	// delete is intentional: a deleted user has no
	// device prefs (their devices are also gone).
	//
	// device_hostname is the lowercase form (matches the
	// v0.28.0 backfill's tag:dev-<user>-<device> naming).
	// The UI normalizes input to lowercase before the
	// INSERT so case differences don't create duplicate
	// rows for the same device.
	//
	// exit_node_tag is the headscale tag (e.g.
	// "tag:exit-relay-3") — NOT the hostname, NOT the
	// node id. The tag is what `grants[].via` takes.
	const q = `CREATE TABLE IF NOT EXISTS device_exit_node_prefs (
		user_id INTEGER NOT NULL,
		device_hostname TEXT NOT NULL,
		exit_node_tag TEXT NOT NULL,
		set_by_user_id INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		PRIMARY KEY (user_id, device_hostname),
		FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
	)`
	if _, err := d.Exec(q); err != nil {
		return err
	}
	return nil
}
