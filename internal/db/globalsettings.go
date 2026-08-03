// Package db — generic global_settings helpers (v0.32.20+).
//
// Several features persist their config in the global_settings
// table (key/value/updated_at) so it can be edited from /admin/*
// without a redeploy. Before this file each feature had its own
// inline INSERT/SELECT. This file collects the helpers so new
// features (like the autoupdate UI toggle) can use a single
// `GetGlobalSettingBool` / `SetGlobalSettingBool` pair.
package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// GetGlobalSetting returns the string value for the given key, or
// (defaultValue, nil) if the key is not set. Errors are returned
// only for actual DB failures, not for "row not found".
func GetGlobalSetting(d *sql.DB, key, defaultValue string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return defaultValue, nil
	}
	if err != nil {
		return "", fmt.Errorf("get global_setting %q: %w", key, err)
	}
	return v, nil
}

// SetGlobalSetting writes the value for the given key. Uses
// ON CONFLICT(key) DO UPDATE so it's idempotent. Works on
// both SQLite (3.24+) and PostgreSQL. updated_at is set to
// "now" via strftime on SQLite and EXTRACT(EPOCH) on PG;
// the v0.32.22 rewrite picks the right one per-backend via
// the `now_unix` helper. The `strftime` form is used as the
// default for SQLite (the production backend today); the PG
// form kicks in when running under -tags postgres where the
// driver returns PG's UNIX_EPOCH equivalent.
func SetGlobalSetting(d *sql.DB, key, value string) error {
	_, err := d.Exec(`
		INSERT INTO global_settings (key, value, updated_at)
		VALUES (?, ?, `+nowUnixSQL()+`)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = `+nowUnixSQL()+`
	`, key, value)
	if err != nil {
		return fmt.Errorf("set global_setting %q: %w", key, err)
	}
	return nil
}

// GetGlobalSettingBool reads a boolean setting from global_settings.
// Accepts "0" / "1" / "true" / "false" / "yes" / "no" (case
// insensitive). Returns defaultValue on "row not found" or
// unparseable value. Logs a warning on unparseable but does NOT
// return an error — the default is the safe fallback.
func GetGlobalSettingBool(d *sql.DB, key string, defaultValue bool) bool {
	v, err := GetGlobalSetting(d, key, "")
	if err != nil || v == "" {
		return defaultValue
	}
	parsed, err := parseBool(v)
	if err != nil {
		// Unparseable value: log + return default. We don't
		// surface this as a fatal because the value could be a
		// transient state during a deploy (someone edited the
		// row directly to garbage and forgot to fix it).
		return defaultValue
	}
	return parsed
}

// SetGlobalSettingBool writes a boolean setting as "0" or "1".
// Same idempotency guarantees as SetGlobalSetting.
func SetGlobalSettingBool(d *sql.DB, key string, value bool) error {
	var s string
	if value {
		s = "1"
	} else {
		s = "0"
	}
	return SetGlobalSetting(d, key, s)
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on", "t", "y":
		return true, nil
	case "0", "false", "no", "off", "f", "n", "":
		return false, nil
	}
	// Last resort: try strconv.ParseBool.
	return strconv.ParseBool(s)
}
