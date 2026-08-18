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
// (defaultValue, nil) if the key is not set OR the stored value
// is the empty string. Errors are returned only for actual DB
// failures, not for "row not found" or "empty value".
//
// v0.33.1.13 — same v0.33.1.12 fix as SetGlobalSetting:
// the SELECT used a hardcoded "?" placeholder, which works on
// SQLite but fails on PG with "syntax error at or near ','"
// (pgx stdlib does NOT auto-convert "?" to "$N"). The new
// placeholdersList(1) helper returns "?" on SQLite and
// "$1" on PG, matching the dispatcher pattern.
// Querier is the minimal interface satisfied by both *sql.DB
// and *sql.Tx. It lets the global_settings helpers be called
// from inside a transaction (e.g. internal/ha.SaveChain) or
// from a plain *sql.DB connection. v1.5.0 (B145) added the
// *sql.Tx variants so the HA chain save can wrap a SELECT
// (change detection) + UPDATE in a single transaction.
type Querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

func GetGlobalSetting(d *sql.DB, key, defaultValue string) (string, error) {
	return getGlobalSetting(qDB{d}, key, defaultValue)
}

// GetGlobalSettingTx is the *sql.Tx variant of GetGlobalSetting.
// Added v1.5.0 (B145) so internal/ha.SaveChain can do an
// atomic SELECT-then-UPDATE without racing concurrent
// /admin/ha editors. See storage.go for the full rationale.
func GetGlobalSettingTx(tx *sql.Tx, key, defaultValue string) (string, error) {
	return getGlobalSetting(qTx{tx}, key, defaultValue)
}

type qDB struct{ D *sql.DB }
type qTx struct{ T *sql.Tx }

func (q qDB) QueryRow(query string, args ...any) *sql.Row { return q.D.QueryRow(query, args...) }
func (q qDB) Exec(query string, args ...any) (sql.Result, error) {
	return q.D.Exec(query, args...)
}
func (q qTx) QueryRow(query string, args ...any) *sql.Row { return q.T.QueryRow(query, args...) }
func (q qTx) Exec(query string, args ...any) (sql.Result, error) {
	return q.T.Exec(query, args...)
}

func getGlobalSetting(q Querier, key, defaultValue string) (string, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM global_settings WHERE key = `+placeholdersList(1), key).Scan(&v)
	if err == sql.ErrNoRows {
		return defaultValue, nil
	}
	if err != nil {
		return "", fmt.Errorf("get global_setting %q: %w", key, err)
	}
	// v0.33.1.13 — also fall back to default when the row
	// exists but the value is the empty string. The Tailscale
	// login-server path relies on this: a "Save URL" click
	// with the input cleared (operator wants the env-var
	// fallback to re-take) writes "" to the DB, and the
	// next read should return the env var (i.e. the default),
	// not an empty string. The SetGlobalSetting path uses an
	// UPSERT, so "delete the row" and "set value=''" both
	// result in a row with value='' — this guard makes both
	// paths equivalent.
	if v == "" {
		return defaultValue, nil
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
//
// v0.33.1.8 — the placeholder syntax was previously "?" for
// both backends, which works on SQLite but PostgreSQL's pgx
// stdlib does NOT auto-convert "?" to "$N" (it would just
// pass the "?" through to PostgreSQL, which rejects it with
// "syntax error at or near ','"). The placeholdersList(n)
// helper returns "$1,$2,..." under -tags postgres and "?,?,..."
// otherwise. The ?,$1,etc split is the same pattern that the
// other DB helpers use (see nowUnixSQL).
func SetGlobalSetting(d *sql.DB, key, value string) error {
	return setGlobalSetting(qDB{d}, key, value)
}

// SetGlobalSettingTx is the *sql.Tx variant of SetGlobalSetting.
// v1.5.0 (B145) — lets internal/ha.SaveChain wrap a SELECT
// (change detection) + UPDATE in a single transaction. See
// storage.go for the full rationale.
func SetGlobalSettingTx(tx *sql.Tx, key, value string) error {
	return setGlobalSetting(qTx{tx}, key, value)
}

func setGlobalSetting(q Querier, key, value string) error {
	_, err := q.Exec(`
		INSERT INTO global_settings (key, value, updated_at)
		VALUES (`+placeholdersList(2)+`, `+nowUnixSQL()+`)
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
