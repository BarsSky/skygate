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
