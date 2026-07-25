// v0.28.4 — per-device preferred exit-node.
//
// 2026-07-25: v0.28.1 added user_exit_node_prefs (one row per
// user) so the operator can pin a user to a specific exit-node.
// v0.28.3 closed the catch-all bypass by making
// `tag:public → autogroup:internet` the only catch-all for
// autogroup:internet — every user can reach the internet
// through their own grant, with via=[<preferred>]. That
// breaks the operator's msi → karolina setup (msi is
// tag:dev-skyadmin-msi → skyadmin@..., and skyadmin's
// per-user via is emilia, so msi is pinned to emilia).
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
//     A user may have a default (emilia) and a single device
//     override (msi → karolina). Both rows can coexist.
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
type DeviceExitNodePref struct {
	UserID          int64
	Username        string
	DeviceHostname  string
	ExitNodeTag     string
	UpdatedAt       int64
	SetByUserID     int64
}

// GetDeviceExitNodePref returns the device's preferred
// exit-node tag, or "" if the device has no per-device
// pref set. Joins portal_users for the username so the
// template can render "<user>-<device>'s preferred
// exit-node: <tag>" without an extra round-trip.
//
// 2026-07-25: v0.28.4.
func GetDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname string) (DeviceExitNodePref, error) {
	var p DeviceExitNodePref
	err := d.QueryRow(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = ? AND p.device_hostname = ?`,
		userID, deviceHostname,
	).Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID)
	if err == sql.ErrNoRows {
		return p, nil
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
// 2026-07-25: v0.28.4.
func SetDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname, exitNodeTag string, setByUserID int64) error {
	if exitNodeTag == "" {
		_, err := d.Exec(`DELETE FROM device_exit_node_prefs WHERE user_id = ? AND device_hostname = ?`,
			userID, deviceHostname)
		return err
	}
	_, err := d.Exec(`
		INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at)
		VALUES (?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(user_id, device_hostname) DO UPDATE SET
			exit_node_tag = excluded.exit_node_tag,
			set_by_user_id = excluded.set_by_user_id,
			updated_at = excluded.updated_at`,
		userID, deviceHostname, exitNodeTag, setByUserID)
	return err
}

// ListAllDeviceExitNodePrefs returns every row, joined
// with portal_users for the username. Used by
// GenerateACLWithViaForPlane to build the per-device
// `via` set in one pass. The result is small (single-
// digit rows in production; only devices the user has
// explicitly pinned) so we don't worry about pagination.
//
// 2026-07-25: v0.28.4.
func ListAllDeviceExitNodePrefs(d *sql.DB) ([]DeviceExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id
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
		if err := rows.Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID); err != nil {
			return nil, err
		}
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
func ListDeviceExitNodePrefsForUser(d *sql.DB, userID int64) ([]DeviceExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = ?
		 ORDER BY p.device_hostname`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceExitNodePref
	for rows.Next() {
		var p DeviceExitNodePref
		if err := rows.Scan(&p.UserID, &p.Username, &p.DeviceHostname, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID); err != nil {
			return nil, err
		}
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
	// "tag:exit-karolina") — NOT the hostname, NOT the
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
