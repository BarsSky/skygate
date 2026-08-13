// Package db — derp_relays.go
//
// v1.3.17: DERP relay CRUD table. Replaces the v0.11.0
// "comma-separated URLs in global_settings" model with a
// first-class relational table so the operator can manage
// multiple relays via the exit-nodes-style /admin/derp/relays
// page (per-row add/edit/delete/toggle/test).
//
// The legacy /admin/derp/config (text-area + bundled checkbox)
// still works: it reads + writes the same
// global_settings.derp.external_urls and
// global_settings.derp.bundled_enabled keys. The two UIs read
// from the same source of truth after AutoMigrateDerpRelays
// (called from LoadIntegrationsFromOS) does a one-shot copy
// of any legacy rows into derp_relays.
//
// "bundled" semantics: at most ONE row can have is_bundled=1
// (the operator's own derper container). AddDerpRelay rejects
// a second bundled row. The bundled row is undeletable from
// the UI (its enable flag is the operator's on/off switch for
// the container), but other rows (external relays from
// third-party providers) can be freely added / edited /
// deleted / toggled.
//
// 2026-08-13: v1.3.17.

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DerpRelay is the row shape for /admin/derp/relays and
// the underlying CRUD functions. The template renders
// every field — see internal/handlers/templates/admin/
// derp_relays.html.
type DerpRelay struct {
	ID         int64  `json:"id"`
	Hostname   string `json:"hostname"`
	URL        string `json:"url"`
	RegionID   int    `json:"region_id"`
	RegionCode string `json:"region_code"`
	RegionName string `json:"region_name"`
	IsBundled  bool   `json:"is_bundled"`
	Enabled    bool   `json:"enabled"`
	SortOrder  int    `json:"sort_order"`
	Notes      string `json:"notes"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// ErrDerpRelayNotFound is returned by GetDerpRelay /
// UpdateDerpRelay / DeleteDerpRelay when the id doesn't
// match any row.
var ErrDerpRelayNotFound = errors.New("derp_relay: not found")

// ErrDerpRelayDuplicateURL is returned by AddDerpRelay when
// a relay with the same URL already exists.
var ErrDerpRelayDuplicateURL = errors.New("derp_relay: duplicate url")

// ErrDerpRelayBundledExists is returned by AddDerpRelay
// when a 2nd is_bundled=1 row is attempted.
var ErrDerpRelayBundledExists = errors.New("derp_relay: a bundled relay already exists")

// ErrDerpRelayBundledUndeletable is returned by
// DeleteDerpRelay when the row is_bundled=1.
var ErrDerpRelayBundledUndeletable = errors.New("derp_relay: bundled row is not deletable")

// ListDerpRelays returns every relay sorted by sort_order,
// then hostname. The bundled row (if any) is always first
// regardless of sort_order so the operator sees it at the
// top of the table.
func ListDerpRelays(d *sql.DB) ([]DerpRelay, error) {
	rows, err := d.Query(`
		SELECT id, hostname, url, region_id, region_code, region_name,
		       is_bundled, enabled, sort_order, notes, created_at, updated_at
		FROM derp_relays
		ORDER BY is_bundled DESC, sort_order ASC, hostname ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list derp_relays: %w", err)
	}
	defer rows.Close()
	var out []DerpRelay
	for rows.Next() {
		var r DerpRelay
		var bundled, enabled int
		if err := rows.Scan(&r.ID, &r.Hostname, &r.URL, &r.RegionID,
			&r.RegionCode, &r.RegionName, &bundled, &enabled,
			&r.SortOrder, &r.Notes, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan derp_relay: %w", err)
		}
		r.IsBundled = bundled != 0
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetDerpRelay returns the row with the given id, or
// ErrDerpRelayNotFound if no such row exists.
func GetDerpRelay(d *sql.DB, id int64) (DerpRelay, error) {
	var r DerpRelay
	var bundled, enabled int
	err := d.QueryRow(`
		SELECT id, hostname, url, region_id, region_code, region_name,
		       is_bundled, enabled, sort_order, notes, created_at, updated_at
		FROM derp_relays WHERE id = $1
	`, id).Scan(&r.ID, &r.Hostname, &r.URL, &r.RegionID,
		&r.RegionCode, &r.RegionName, &bundled, &enabled,
		&r.SortOrder, &r.Notes, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DerpRelay{}, ErrDerpRelayNotFound
	}
	if err != nil {
		return DerpRelay{}, fmt.Errorf("get derp_relay %d: %w", id, err)
	}
	r.IsBundled = bundled != 0
	r.Enabled = enabled != 0
	return r, nil
}

// AddDerpRelay inserts a new row. Rejects:
//   - duplicate url (UNIQUE constraint → ErrDerpRelayDuplicateURL)
//   - a 2nd is_bundled=1 row → ErrDerpRelayBundledExists
//
// url is required. Hostname defaults to the URL's host if
// empty. region_id defaults to 900 if 0.
func AddDerpRelay(d *sql.DB, r DerpRelay) (int64, error) {
	if strings.TrimSpace(r.URL) == "" {
		return 0, errors.New("derp_relay: url is required")
	}
	if r.RegionID == 0 {
		r.RegionID = 900
	}
	if strings.TrimSpace(r.Hostname) == "" {
		r.Hostname = hostnameFromURL(r.URL)
	}
	if r.SortOrder == 0 {
		r.SortOrder = 100
	}
	if r.IsBundled {
		var n int
		if err := d.QueryRow(
			`SELECT count(*) FROM derp_relays WHERE is_bundled = 1`,
		).Scan(&n); err != nil {
			return 0, fmt.Errorf("check bundled: %w", err)
		}
		if n > 0 {
			return 0, ErrDerpRelayBundledExists
		}
	}
	now := time.Now().Unix()
	var id int64
	err := d.QueryRow(`
		INSERT INTO derp_relays
			(hostname, url, region_id, region_code, region_name,
			 is_bundled, enabled, sort_order, notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
		RETURNING id
	`, r.Hostname, r.URL, r.RegionID, r.RegionCode, r.RegionName,
		boolToInt(r.IsBundled), boolToInt(r.Enabled),
		r.SortOrder, r.Notes, now).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDerpRelayDuplicateURL
		}
		return 0, fmt.Errorf("insert derp_relay: %w", err)
	}
	return id, nil
}

// UpdateDerpRelay writes the editable fields (everything
// except id, created_at, is_bundled) for an existing row.
// is_bundled is intentionally NOT editable from the UI —
// the operator can only enable / disable the bundled row,
// not convert an external relay into the bundled one (or
// vice versa). Use the Add + Delete flow for that (with
// deploy.sh if the bundled container needs recreation).
func UpdateDerpRelay(d *sql.DB, r DerpRelay) error {
	res, err := d.Exec(`
		UPDATE derp_relays SET
			hostname = $2, url = $3,
			region_id = $4, region_code = $5, region_name = $6,
			enabled = $7, sort_order = $8, notes = $9,
			updated_at = $10
		WHERE id = $1
	`, r.ID, r.Hostname, r.URL, r.RegionID, r.RegionCode, r.RegionName,
		boolToInt(r.Enabled), r.SortOrder, r.Notes, time.Now().Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDerpRelayDuplicateURL
		}
		return fmt.Errorf("update derp_relay: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update derp_relay rows: %w", err)
	}
	if n == 0 {
		return ErrDerpRelayNotFound
	}
	return nil
}

// ToggleDerpRelayEnabled flips the enabled flag for the
// given id. The bundled row is freely toggleable (that's
// the operator's on/off switch for the container).
func ToggleDerpRelayEnabled(d *sql.DB, id int64) (DerpRelay, error) {
	res, err := d.Exec(`
		UPDATE derp_relays SET enabled = 1 - enabled, updated_at = $2
		WHERE id = $1
	`, id, time.Now().Unix())
	if err != nil {
		return DerpRelay{}, fmt.Errorf("toggle derp_relay: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return DerpRelay{}, fmt.Errorf("toggle derp_relay rows: %w", err)
	}
	if n == 0 {
		return DerpRelay{}, ErrDerpRelayNotFound
	}
	return GetDerpRelay(d, id)
}

// DeleteDerpRelay removes the row. Refuses to delete the
// bundled row (ErrDerpRelayBundledUndeletable) — the
// operator should toggle its enabled flag instead.
func DeleteDerpRelay(d *sql.DB, id int64) error {
	// Look up first so we can reject the bundled case
	// with a clear error rather than a silent no-op.
	row, err := GetDerpRelay(d, id)
	if err != nil {
		return err
	}
	if row.IsBundled {
		return ErrDerpRelayBundledUndeletable
	}
	res, err := d.Exec(`DELETE FROM derp_relays WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete derp_relay: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete derp_relay rows: %w", err)
	}
	if n == 0 {
		return ErrDerpRelayNotFound
	}
	return nil
}

// ListEnabledDerpRelayURLs returns the URL of every
// enabled relay. Used by the headscale-config renderer
// (integrations_renderer.go) to build the derp.urls list
// — replaces the old comma-separated global_settings
// read. The bundled relay is included if enabled.
func ListEnabledDerpRelayURLs(d *sql.DB) ([]string, error) {
	rows, err := d.Query(`
		SELECT url FROM derp_relays
		WHERE enabled = 1
		ORDER BY is_bundled DESC, sort_order ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list enabled derp_relays: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan derp_relay url: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// IsBundledDerpRelayEnabled returns true if the bundled
// row exists and is enabled. Used by applyBundledDERP
// to decide whether to start / stop the derper container.
func IsBundledDerpRelayEnabled(d *sql.DB) (bool, error) {
	var enabled int
	err := d.QueryRow(`
		SELECT enabled FROM derp_relays WHERE is_bundled = 1 LIMIT 1
	`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // no bundled row at all
	}
	if err != nil {
		return false, fmt.Errorf("is bundled enabled: %w", err)
	}
	return enabled != 0, nil
}

// AutoMigrateDerpRelays is the one-shot backward-compat
// bridge from the v0.11.0 global_settings model. Called
// from LoadIntegrationsFromOS on every read; it's idempotent
// (a global_settings key "derp.relays_migrated" = "1" marker
// makes it a no-op after the first run).
//
// Migration rules:
//   - If derp_relays is non-empty, mark migrated and return.
//   - Read global_settings.derp.external_urls (CSV) — each
//     URL becomes a row with is_bundled=0, region_id=901+,
//     sort_order=100+i.
//   - If global_settings.derp.bundled_enabled == "1", add
//     a row with is_bundled=1, region_id=900, sort_order=10
//     (always above the external rows).
//   - If global_settings.derp.bundled_enabled == "0" (or
//     unset), do NOT add a bundled row. The operator can
//     add it from the UI if they want a bundled relay.
//   - Write "derp.relays_migrated"="1" to mark done.
func AutoMigrateDerpRelays(d *sql.DB) error {
	// Already migrated? No-op.
	if v := loadGlobalSetting(d, "derp.relays_migrated"); v == "1" {
		return nil
	}
	// Already has rows? (Operator may have used the new
	// UI before this helper ran, e.g. by manual INSERT.)
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM derp_relays`,
	).Scan(&n); err != nil {
		return fmt.Errorf("auto-migrate count: %w", err)
	}
	if n > 0 {
		_ = saveGlobalSetting(d, "derp.relays_migrated", "1")
		return nil
	}
	// Read legacy keys.
	urlsRaw := loadGlobalSetting(d, "derp.external_urls")
	bundled := loadGlobalSetting(d, "derp.bundled_enabled") == "1"

	if bundled {
		// Bundled row first, sort_order=10 so it sorts
		// above the external rows.
		now := time.Now().Unix()
		_, err := d.Exec(`
			INSERT INTO derp_relays
				(hostname, url, region_id, region_code, region_name,
				 is_bundled, enabled, sort_order, notes, created_at, updated_at)
			VALUES ('Skygate DERP', '', 900, 'custom', 'Skygate DERP',
			        1, 1, 10, 'migrated from derp.bundled_enabled=1', $1, $1)
		`, now)
		if err != nil && !isUniqueViolation(err) {
			return fmt.Errorf("auto-migrate bundled: %w", err)
		}
	}
	if urlsRaw != "" {
		now := time.Now().Unix()
		for i, u := range strings.Split(urlsRaw, ",") {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			// External rows start at sort_order=100,
			// with the i-th row offset by 10 to preserve
			// the v0.11.0 textarea order.
			_, err := d.Exec(`
				INSERT INTO derp_relays
					(hostname, url, region_id, region_code, region_name,
					 is_bundled, enabled, sort_order, notes, created_at, updated_at)
				VALUES ($1, $2, $3, '', '', 0, 1, $4, 'migrated from derp.external_urls', $5, $5)
			`, hostnameFromURL(u), u, 901+i, 100+10*i, now)
			if err != nil && !isUniqueViolation(err) {
				return fmt.Errorf("auto-migrate %s: %w", u, err)
			}
		}
	}
	if err := saveGlobalSetting(d, "derp.relays_migrated", "1"); err != nil {
		return fmt.Errorf("auto-migrate marker: %w", err)
	}
	return nil
}

// hostnameFromURL extracts the host portion of a URL, or
// returns the whole string if parsing fails. Used as the
// default Hostname for new rows + the migrated-from-legacy
// rows where the v0.11.0 model didn't track hostnames.
func hostnameFromURL(u string) string {
	// Trim scheme if present
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	// Trim path
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	// Trim port
	if i := strings.Index(u, ":"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return "unknown"
	}
	return u
}

// boolToInt is the int(0/1) conversion for the enabled +
// is_bundled columns. Defined in exit_node_health.go.

// isUniqueViolation reports whether err is a PG
// "duplicate key violates unique constraint" error. Used
// to translate AddDerpRelay / UpdateDerpRelay's raw
// driver error into the friendly ErrDerpRelayDuplicateURL.
//
// pgx surfaces unique-violation as a *pgconn.PgError with
// Code "23505". We do a string check so the helper stays
// free of pgx-specific types (the caller may be using
// database/sql with the pgx stdlib driver — same error
// comes through as a string).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "23505") || strings.Contains(s, "unique constraint")
}

// MustParseInt is a defensive helper that returns 0 for
// empty strings. Used by handlers that bind sort_order
// from form values.
func MustParseInt(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
