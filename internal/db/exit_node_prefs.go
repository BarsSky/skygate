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

import (
	"database/sql"
	"fmt"
	"strings"
)

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

// DeleteDeviceExitNodePref removes the per-device pref for
// (user_id, device_hostname). Used by the /my/devices delete
// flow (B162) to clean up the pref row when the device is
// removed from headscale. No-op if the row doesn't exist
// (PostgreSQL DELETE silently succeeds on 0 rows). The
// caller is expected to use the lowercased hostname (the
// device_exit_node_prefs table stores the lowercased form
// to match the v0.28.0 tag:dev-<user>-<device> convention).
func DeleteDeviceExitNodePref(d *sql.DB, userID int64, deviceHostname string) error {
	_, err := d.Exec(
		`DELETE FROM device_exit_node_prefs WHERE user_id = $1 AND device_hostname = $2`,
		userID, deviceHostname,
	)
	return err
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

// NormalizeExitNodeTag — v1.5.2 (B188) — converts a
// free-text exit-node tag (typically submitted by a form
// dropdown) into the canonical headscale tag stored in
// node_owner_map.
//
// The form templates historically built the tag with
// `tag:exit-<hostname>` (the LEGACY convention, before
// the v0.33.1.39 / B118 cutover to `tag:dev-infra-<host>`).
// Live data (2026-08-25 audit) confirms 2 rows still
// carry the legacy form: a71 → "tag:exit-emilia" and
// basic → "tag:exit-emilia". The tag:exit-emilia string
// is NOT a real headscale tag (it's not in policy
// tagOwners), so the via=[...] grant headscale sees
// references a non-existent tag and the policy either
// silently no-ops on the packet-filter side OR is
// rejected outright by the v2 parser.
//
// NormalizeExitNodeTag is the single source of truth
// for "given a hostname / given_name, what is the real
// headscale tag?". It queries node_owner_map
// (case-insensitive on hostname) and returns the .tag
// column verbatim. The two callers (the per-device
// POST handler and the per-user POST handler) invoke
// it BEFORE the DB write so the persisted value is
// always the canonical form, regardless of what the
// template sends.
//
// Returns "" when:
//   - hostname is empty (caller wants to clear the pref)
//   - no node_owner_map row matches (unknown device;
//     the handler logs a warning and refuses the write
//     so a typo doesn't silently insert a ghost tag)
//   - the node_owner_map row's tag is a USER-DEVICE dev tag
//     like "tag:dev-michail-basic" rather than an exit-node
//     infra tag like "tag:dev-infra-emilia" — TD-17.1 fix
//     (see internal/feature/exit_rules/td17_history.md
//     for the michail/basic data-corruption case)
//
// The function is intentionally read-only (it does not
// modify node_owner_map). The B188 migration (see
// migrateV061PG) handles the one-time backfill of
// legacy tag:exit-X rows already in the prefs tables.
//
// 2026-08-27: TD-17.1 — added tag-form check so a user-device's
// own dev tag (tag:dev-<user>-<host>) cannot be stored as
// an exit-node preference. The function returns
// ErrUserDeviceDevTagNotExitNode in that case, which the
// handler turns into 400.
//
// 2026-08-26: v1.5.2 (B188).
func NormalizeExitNodeTag(d *sql.DB, hostname string) (string, error) {
	if hostname == "" {
		return "", nil
	}
	var tag string
	err := d.QueryRow(
		`SELECT tag FROM node_owner_map WHERE LOWER(hostname) = `+PlaceholdersList(1)+` LIMIT 1`,
		strings.ToLower(hostname),
	).Scan(&tag)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// TD-17.1: reject user-device dev tags. node_owner_map stores
	// the dev tag for every node, but only "tag:dev-infra-X" or
	// legacy "tag:exit-X" forms are valid exit-node tags. The
	// user-device form "tag:dev-<user>-<host>" is what B175's
	// node-ownership strategy writes for every user device
	// (e.g. tag:dev-michail-basic for michail's basic). If we
	// returned it here, a user could "pin" a device to itself
	// (basic → tag:dev-michail-basic) — the via= grant would
	// resolve to the device's own dev tag, the policy would
	// be self-referential, and the device would never reach the
	// actual exit node. The 2026-08-27 michail/basic case
	// proved this happens via the UI when the dropdown is
	// small or the user selects the source device's own tag.
	if !isExitNodeTagForm(tag) {
		return "", fmt.Errorf("%w: got %q for hostname %q (looks like a user-device dev tag, not an exit-node infra tag); use tag:dev-infra-<exit-hostname> or tag:exit-<exit-hostname> instead", ErrUserDeviceDevTagNotExitNode, tag, hostname)
	}
	return tag, nil
}

// isExitNodeTagForm reports whether `tag` matches one of the
// canonical exit-node tag forms:
//   - "tag:dev-infra-<host>"  (B111+ infra form — emilia, karolina, sharlotta)
//   - "tag:exit-<host>"        (legacy pre-B93 form, still accepted)
// Anything else (in particular "tag:dev-<user>-<host>" — the
// user-device dev tag written by B175) returns false.
//
// 2026-08-27: TD-17.1.
func isExitNodeTagForm(tag string) bool {
	t := strings.TrimSpace(tag)
	switch {
	case strings.HasPrefix(t, "tag:dev-infra-"):
		return true
	case strings.HasPrefix(t, "tag:exit-"):
		return true
	default:
		return false
	}
}

// ResolveExitNodeTag normalises the form's tag value against
// node_owner_map. It is the single entry point for the 3 form
// handlers (PostMyDevicePreferredExit, PostAdminDevicePreferredExit,
// PostMyExitNodePreferred) that store exit-node prefs.
//
// Semantics:
//   - empty rawTag  -> returns ("", nil). The caller is
//     clearing the pref; no DB lookup needed. The Pref helpers
//     interpret "" as "DELETE the row".
//   - non-empty rawTag -> looks up the canonical tag for hostname.
//     If the hostname isn't in node_owner_map, returns
//     ("", ErrNoSuchDevice) so the caller can return 400 with
//     a precise error message. Otherwise returns the canonical
//     tag (which is identical to rawTag in 99% of cases — the
//     function is "fail-pass-through" rather than "fail-correct").
//
// hostname is the device hostname from the form (the handler
// lowercases before calling). Caller doesn't need to lowercase
// again — we do it inside the SQL via LOWER(hostname).
//
// 2026-08-26: v1.5.2 (B188 refactor). Replaces 3 copy-pasted
// 16-line blocks in the 3 form handlers.
func ResolveExitNodeTag(d *sql.DB, hostname, rawTag string) (string, error) {
	if rawTag == "" {
		return "", nil
	}
	canonicalTag, err := NormalizeExitNodeTag(d, hostname)
	if err != nil {
		return "", fmt.Errorf("tag normalization: %w", err)
	}
	if canonicalTag == "" {
		return "", ErrNoSuchExitNodeDevice
	}
	return canonicalTag, nil
}

// ErrNoSuchExitNodeDevice is returned by ResolveExitNodeTag
// when the form is being set (rawTag != "") but the hostname
// doesn't resolve in node_owner_map. The handler turns this
// into a 400 with a precise message so a typo in the dropdown
// doesn't silently insert a ghost tag.
var ErrNoSuchExitNodeDevice = fmt.Errorf("device not found in node_owner_map; cannot resolve exit-node tag")

// ErrUserDeviceDevTagNotExitNode is returned by
// NormalizeExitNodeTag when the node_owner_map row's tag is
// a user-device dev tag ("tag:dev-<user>-<host>") rather than
// an exit-node infra tag ("tag:dev-infra-<host>"). This was
// the TD-17.1 bug: the 2026-08-27 michail/basic case stored
// "tag:dev-michail-basic" as basic's preferred exit-node,
// making the via= grant self-referential. The handler
// surfaces this error as 400 with the same form-rejection
// path as ErrNoSuchExitNodeDevice.
// 2026-08-27: TD-17.1.
var ErrUserDeviceDevTagNotExitNode = fmt.Errorf("hostname found in node_owner_map but its tag is a user-device dev tag, not an exit-node infra tag")
