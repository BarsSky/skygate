// exit_node_prefs.go — v0.28.1+ per-user and per-device
// preferred exit-node helpers. Extracted from the deleted
// migrations_v0.45.go / migrations_v0.46.go (SQLite-style
// migrations that no longer exist in v1.3.0 since skygate is
// PG-only and the same schema is created by migrateV045PG /
// migrateV046PG in migrations_pg.go).
//
// The helpers are read/write against the tables created by
// the PG migration chain. They use the same per-backend
// placeholder helpers (PlaceholdersList, PlaceholderAt,
// PlaceholdersRange, nowUnixSQL) so the queries work on PG
// without further conversion.
package db

import "database/sql"

// ExitNodePref is the per-user preferred exit-node + strict-pinning
// flag. See SetUserExitNodePref for the contract.
type ExitNodePref struct {
	UserID      int64
	Username    string
	ExitNodeTag string
	UpdatedAt   int64
	SetByUserID int64
	ViaEnabled  bool
}

// DeviceExitNodePref is the per-device preferred exit-node +
// strict-pinning flag. See SetDeviceExitNodePref for the
// contract.
type DeviceExitNodePref struct {
	UserID         int64
	Username       string
	DeviceHostname string
	ExitNodeTag    string
	UpdatedAt      int64
	SetByUserID    int64
	ViaEnabled     bool
}

// GetUserExitNodePref returns the user's preferred exit-node
// tag, or "" if the user has no preference set. Joins
// portal_users for the username so the template can render
// "<username>'s preferred exit-node: <tag>" without an
// extra round-trip.
//
// 2026-07-24: v0.28.1.
// 2026-07-25: v0.28.5 — also returns ViaEnabled so the
// caller can show the strict-pinning state in the UI.
func GetUserExitNodePref(d *sql.DB, userID int64) (ExitNodePref, error) {
	var p ExitNodePref
	var viaEnabled int
	err := d.QueryRow(`
		SELECT p.user_id, pu.username, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM user_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = `+PlaceholdersList(1), userID,
	).Scan(&p.UserID, &p.Username, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err == nil {
		p.ViaEnabled = viaEnabled != 0
	}
	return p, err
}

// SetUserExitNodePref upserts the user's preferred exit-node.
// Pass exitNodeTag = "" to clear the preference (the row is
// deleted, not set to empty — keeps the table small and
// makes "has preference?" a simple SELECT EXISTS check).
//
// setByUserID is recorded in the row so the admin can see
// "alice set this herself" vs. "admin set this for alice"
// on /admin/users/{id}/subnet.
//
// 2026-07-25: v0.28.5 — added viaEnabled param. When true,
// the ACL builder emits via=[] in the per-user grant. When
// false (the new default for fresh rows), the per-user
// grant has dst=autogroup:internet with no via (Android-
// friendly). Existing rows are migrated to via_enabled=1
// (backwards compat with v0.28.1-v0.28.4 — the operator
// has to explicitly flip to 0 to un-pin).
func SetUserExitNodePref(d *sql.DB, userID int64, exitNodeTag string, setByUserID int64, viaEnabled bool) error {
	if exitNodeTag == "" {
		_, err := d.Exec(`DELETE FROM user_exit_node_prefs WHERE user_id = `+PlaceholdersList(1), userID)
		return err
	}
	viaInt := 0
	if viaEnabled {
		viaInt = 1
	}
	_, err := d.Exec(`
		INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (`+PlaceholdersRange(1, 3)+`, `+NowUnixSQL()+`, `+PlaceholdersRange(4, 4)+`)
		ON CONFLICT(user_id) DO UPDATE SET
			exit_node_tag = excluded.exit_node_tag,
			set_by_user_id = excluded.set_by_user_id,
			updated_at = excluded.updated_at,
			via_enabled = excluded.via_enabled`,
		userID, exitNodeTag, setByUserID, viaInt)
	return err
}

// ListAllUserExitNodePrefs returns every row, joined with
// portal_users for the username. Used by GenerateACLWithVia
// to build the per-user `via` set in one pass. The result
// is small (4-10 rows in production) so we don't worry
// about pagination.
//
// 2026-07-25: v0.28.5 — also returns ViaEnabled. The ACL
// builder uses this to decide whether to emit via=[] in
// the per-user grant (via_enabled=true) or skip the via
// (via_enabled=false — the user has a "default" exit-node
// for the UI but no policy-level pinning).
func ListAllUserExitNodePrefs(d *sql.DB) ([]ExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM user_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 ORDER BY pu.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExitNodePref
	for rows.Next() {
		var p ExitNodePref
		var viaEnabled int
		if err := rows.Scan(&p.UserID, &p.Username, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled); err != nil {
			return nil, err
		}
		p.ViaEnabled = viaEnabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
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
// exit-node. Pass exitNodeTag = "" to clear the preference.
// See SetUserExitNodePref for the contract / via_enabled
// semantics; the only difference is the PRIMARY KEY is
// (user_id, device_hostname) instead of (user_id).
func SetDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname, exitNodeTag string, setByUserID int64, viaEnabled bool) error {
	if exitNodeTag == "" {
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
		VALUES (`+PlaceholdersRange(1, 4)+`, `+NowUnixSQL()+`, `+PlaceholdersRange(5, 5)+`)
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

// ListDeviceExitNodePrefsForUser returns the device exit-node
// prefs for a single user. Same shape as ListAllDeviceExitNodePrefs
// but filtered to one user (the UI calls this for /my/exit-nodes
// without an extra JOIN). Returns nil (not an error) when
// the user has no per-device prefs.
func ListDeviceExitNodePrefsForUser(d *sql.DB, userID int64) ([]DeviceExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.device_hostname, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM device_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = `+PlaceholdersList(1)+`
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
