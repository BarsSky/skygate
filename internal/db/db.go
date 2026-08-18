package db

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type User struct {
	ID                 int64
	Username           string
	IsAdmin            bool
	Theme              string
	PasswordHash       string
	HeadscaleUserID    int64
	CreatedAt          time.Time
	SubnetCIDR         string // denorm: empty if no subnet allocated
	SubnetStatus       string // denorm: "none" / "pending" / "active" / "disabled"
	SubnetRouterNodeID int64  // denorm: 0 if no router provisioned (v0.16.7+)
}

const (
	ThemeLinear = "linear"
	ThemeVercel = "vercel"
	ThemeSentry = "sentry"
	ThemeNvidia = "nvidia"
	// 2026-08-17: v1.3.19.2 follow-up (B121) — Mint theme. Light
	// theme with silver background + mint-green accent. Designed
	// for long admin sessions: high-contrast text, soft borders,
	// mint accent that's easy on the eyes (vs the high-saturation
	// indigo of Linear). Pairs with the form-contrast improvements
	// to the Linear theme (also in B121).
	ThemeMint = "mint"
)

func ThemeLabel(t string) string {
	switch t {
	case ThemeLinear:
		return "Linear"
	case ThemeVercel:
		return "Vercel"
	case ThemeSentry:
		return "Sentry"
	case ThemeNvidia:
		return "NVIDIA"
	case ThemeMint:
		return "Mint"
	default:
		return "Linear"
	}
}

func IsValidTheme(t string) bool {
	switch t {
	case ThemeLinear, ThemeVercel, ThemeSentry, ThemeNvidia, ThemeMint:
		return true
	}
	return false
}

func GetUserTheme(d *sql.DB, userID int64) string {
	var theme string
	err := d.QueryRow("SELECT COALESCE(theme, 'dark') FROM portal_users WHERE id = $1", userID).Scan(&theme)
	if err != nil || !IsValidTheme(theme) {
		return ThemeLinear
	}
	return theme
}

func SetUserTheme(d *sql.DB, userID int64, theme string) error {
	_, err := d.Exec("UPDATE portal_users SET theme = $1 WHERE id = $2", theme, userID)
	return err
}

// Display preference constants for B136. The font_family values
// are the keys that map to CSS font-family declarations (the
// layout.html injects the right @font-face / Google Fonts link
// for the chosen family). font_scale is a delta in px applied
// to the body font-size (15px default in v1.3.20.5; the theme
// is the baseline, this is a per-user shift on top of it).
const (
	FontFamilyManrope = "manrope"
	FontFamilyInter   = "inter"
	FontFamilyGeist   = "geist"
	FontFamilySora    = "sora"
	FontFamilySystem  = "system" // system-ui only, no Google Fonts

	FontScaleMin = -2
	FontScaleMax = 2
)

// IsValidFontFamily returns true for known font families.
// Unknown values fall back to FontFamilyManrope.
func IsValidFontFamily(s string) bool {
	switch s {
	case FontFamilyManrope, FontFamilyInter, FontFamilyGeist, FontFamilySora, FontFamilySystem:
		return true
	}
	return false
}

// FontFamilyLabel returns a human-readable name for the font
// family code. Used by the /my/account dropdown so the operator
// sees "Manrope" instead of "manrope".
func FontFamilyLabel(s string) string {
	switch s {
	case FontFamilyManrope:
		return "Manrope"
	case FontFamilyInter:
		return "Inter"
	case FontFamilyGeist:
		return "Geist"
	case FontFamilySora:
		return "Sora"
	case FontFamilySystem:
		return "System (no web font)"
	}
	return "Manrope"
}

// ClampFontScale clamps a user-supplied font_scale value into
// the [-2, +2] range. Values outside the range default to 0.
func ClampFontScale(n int) int {
	if n < FontScaleMin {
		return 0
	}
	if n > FontScaleMax {
		return 0
	}
	return n
}

// DisplayPrefs is the per-user display configuration persisted
// in portal_users. Used by the layout template to inject the
// right <style> block in <head>.
type DisplayPrefs struct {
	FontFamily  string // "manrope" | "inter" | "geist" | "sora" | "system"
	FontScale   int    // -2..+2 px delta on body font-size
	SelectionBg string // CSS color for ::selection, "" = theme default
}

// GetUserDisplayPrefs reads font_family / font_scale /
// selection_bg for a user. Falls back to defaults if the user
// has no row yet, or the values are missing/invalid (older
// schemas that pre-date B136).
func GetUserDisplayPrefs(d *sql.DB, userID int64) DisplayPrefs {
	var p DisplayPrefs
	// 3 separate COALESCE — if a column is missing (older DB before
	// V057), the COALESCE returns the default. The "always return
	// one row" pattern (no rows.Err check) is fine here because
	// the user_id is from a JWT — if the row doesn't exist, the
	// user is in a bad state but we return defaults either way.
	err := d.QueryRow(`
		SELECT
			COALESCE(font_family, 'manrope'),
			COALESCE(font_scale, 0),
			COALESCE(selection_bg, '')
		FROM portal_users WHERE id = $1
	`, userID).Scan(&p.FontFamily, &p.FontScale, &p.SelectionBg)
	if err != nil {
		return DisplayPrefs{FontFamily: FontFamilyManrope, FontScale: 0, SelectionBg: ""}
	}
	if !IsValidFontFamily(p.FontFamily) {
		p.FontFamily = FontFamilyManrope
	}
	p.FontScale = ClampFontScale(p.FontScale)
	return p
}

// SetUserDisplayPrefs writes the per-user display prefs.
// font_family is validated (invalid → FontFamilyManrope) and
// font_scale is clamped. selection_bg is stored as-is (it's
// either empty or a CSS color the user typed).
func SetUserDisplayPrefs(d *sql.DB, userID int64, p DisplayPrefs) error {
	if !IsValidFontFamily(p.FontFamily) {
		p.FontFamily = FontFamilyManrope
	}
	p.FontScale = ClampFontScale(p.FontScale)
	// selection_bg is intentionally NOT validated — accept any
	// CSS color string. If the user types garbage, the browser
	// silently ignores the invalid value (CSS is forgiving).
	_, err := d.Exec(`
		UPDATE portal_users
		SET font_family = $1, font_scale = $2, selection_bg = $3
		WHERE id = $4
	`, p.FontFamily, p.FontScale, p.SelectionBg, userID)
	return err
}

// OpenForTest opens a fresh test PG DB (uses a unique schema per
// test for isolation) with the full production migration chain
// applied. v1.3.0: skygate is PG-only; tests skip when
// SKYGATE_TEST_PG_DSN is unset (no SQLite fallback).
//
// OpenForTest is the canonical helper for tests that previously
// used a per-package `sql.Open("sqlite3", ":memory:")` and need a
// real schema. It runs the same migration chain as production
// (MigratePostgres) so the test sees the same table layout.
func OpenForTest(t *testing.T) *sql.DB {
	t.Helper()
	dsn := pgTestDSN()
	if dsn == "" {
		t.Skip(skipPGMessage)
		return nil // unreachable
	}
	return OpenTestPG(t)
}

// OpenDSN opens a PostgreSQL connection. v1.3.0: skygate is
// PG-only; the dsn is the standard libpq URL form:
//
//	postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
//
// Pool sizing follows the small-Go-HTTP-service defaults: 10 open /
// 5 idle. Operators tune via the DSN's `pool_max_conns` parameter
// (pgx-native). MigratePostgres is called on every Open so the
// container start re-applies the idempotent migration chain.
func OpenDSN(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	if err := MigratePostgres(conn); err != nil {
		conn.Close()
		return nil, err
	}
	registerBackend(conn, BackendPostgres)
	return conn, nil
}
