// Package db — user preference helpers.
//
// 2026-07-13: Этап 11 part 2a. Two per-user preferences are stored
// as TEXT columns on portal_users (migration v0.30):
//
//   default_device_node_id   — the headscale node_id the user has
//                              picked as their default device (for
//                              /add_rule and any future
//                              "which device?" shortcut).
//   default_exit_node_id     — the headscale node_id of the user's
//                              default exit-node.
//
// Empty string ("") is the canonical "no default" sentinel — both
// the DB columns and the helper return values use it. Callers
// don't need to special-case NULL; the COALESCE in the read
// helpers turns a NULL (which can't actually occur because the
// columns are NOT NULL DEFAULT '') into "" for the same effect.
//
// We chose columns over a generic user_prefs table because the
// shape is fixed (two strings per user), the columns are tiny,
// and the helper boundary stays narrow (4 small functions
// instead of 4 small functions + a key/value layer). The
// `theme` column precedent on portal_users also argues for
// staying in the same shape.

package db

import "database/sql"

// GetDefaultDevice returns the user's default device node_id, or
// "" when no default is set. Returns ErrUserNotFound if the user
// doesn't exist — callers that need to distinguish "no default"
// from "no user" can branch on errors.Is; callers that just want
// the node_id can use the empty-string check alone.
func GetDefaultDevice(d *sql.DB, userID int64) (string, error) {
	var s string
	err := d.QueryRow(
		`SELECT COALESCE(default_device_node_id, '') FROM portal_users WHERE id = ?`,
		userID,
	).Scan(&s)
	if err == sql.ErrNoRows {
		return "", ErrUserNotFound
	}
	return s, err
}

// SetDefaultDevice sets the user's default device node_id.
// Empty string is a valid value (clears the default). Returns
// the number of rows affected (0 if the user vanished between
// auth and update — caller's choice whether to surface that).
func SetDefaultDevice(d *sql.DB, userID int64, nodeID string) (int64, error) {
	res, err := d.Exec(
		`UPDATE portal_users SET default_device_node_id = ? WHERE id = ?`,
		nodeID, userID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetDefaultExitNode returns the user's default exit-node node_id,
// or "" when no default is set. Symmetric with GetDefaultDevice
// (same return convention, same ErrUserNotFound behaviour).
func GetDefaultExitNode(d *sql.DB, userID int64) (string, error) {
	var s string
	err := d.QueryRow(
		`SELECT COALESCE(default_exit_node_id, '') FROM portal_users WHERE id = ?`,
		userID,
	).Scan(&s)
	if err == sql.ErrNoRows {
		return "", ErrUserNotFound
	}
	return s, err
}

// SetDefaultExitNode sets the user's default exit-node node_id.
// Symmetric with SetDefaultDevice.
func SetDefaultExitNode(d *sql.DB, userID int64, nodeID string) (int64, error) {
	res, err := d.Exec(
		`UPDATE portal_users SET default_exit_node_id = ? WHERE id = ?`,
		nodeID, userID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
