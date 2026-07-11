package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
	_ "github.com/mattn/go-sqlite3"
)

type User struct {
	ID              int64
	Username        string
	IsAdmin         bool
	Theme           string
	PasswordHash    string
	HeadscaleUserID int64
	CreatedAt       time.Time
}

const (
	ThemeLinear = "linear"
	ThemeVercel = "vercel"
	ThemeSentry = "sentry"
	ThemeNvidia = "nvidia"
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
	default:
		return "Linear"
	}
}

func IsValidTheme(t string) bool {
	switch t {
	case ThemeLinear, ThemeVercel, ThemeSentry, ThemeNvidia:
		return true
	}
	return false
}

func GetUserTheme(d *sql.DB, userID int64) string {
	var theme string
	err := d.QueryRow("SELECT COALESCE(theme, 'dark') FROM portal_users WHERE id = ?", userID).Scan(&theme)
	if err != nil || !IsValidTheme(theme) {
		return ThemeLinear
	}
	return theme
}

func SetUserTheme(d *sql.DB, userID int64, theme string) error {
	_, err := d.Exec("UPDATE portal_users SET theme = ? WHERE id = ?", theme, userID)
	return err
}

func Open(dataDir string) (*sql.DB, error) {
	dbPath := dataDir
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	// 2026-07-09: refactor v0.6.0 — Open() now bootstraps schema. Migrations
	// are idempotent (CREATE TABLE IF NOT EXISTS + ALTER with duplicate-column
	// guards) so calling migrate() on every Open is safe and matches what
	// fresh deployments + unit tests expect.
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return conn, nil
}

func migrate(d *sql.DB) error {
	queries := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	// 2026-07-11: Этап 9 part 2 — fixed migration ordering. The
	// 2026-07-09 refactor moved V020 (CREATE device_rules + friends) to
	// AFTER V021/V022 (ALTER device_rules), which made the ALTERs no-ops
	// (the table didn't exist yet) and then V020 created device_rules
	// WITHOUT the action + device_ip columns. The bug was latent
	// because the VM DB was bootstrapped under the old order; only a
	// fresh DB exposes it (which the new db_helpers_part2_test.go
	// does). Correct order:
	//
	//   V025 — portal_users + friends (FK target for everything else)
	//   V020 — CREATE device_rules / exit_servers / acl_snapshots / exit_rule_logs
	//   V021 — ALTER device_rules ADD action + global_settings
	//   V022 — ALTER device_rules ADD device_ip
	//   V023 — CREATE personal_api_tokens (FK → portal_users, already exists)
	//   V024 — ALTER exit_servers (needs exit_servers, already exists)
	//   V026 — ALTER exit_servers ADD accept_routes (needs V024 done)
	//   V027 — CREATE telegram_alerts (independent)
	migrateV025(d)
	if err := migrateV020(d); err != nil {
		return fmt.Errorf("migrate v0.20: %w", err)
	}
	if err := migrateV021(d); err != nil {
		return fmt.Errorf("migrate v0.21: %w", err)
	}
	if err := migrateV022(d); err != nil {
		return fmt.Errorf("migrate v0.22: %w", err)
	}
	if err := migrateV023(d); err != nil {
		return fmt.Errorf("migrate v0.23: %w", err)
	}
	if err := migrateV024(d); err != nil {
		return fmt.Errorf("migrate v0.24: %w", err)
	}
	if err := migrateV026(d); err != nil {
		return fmt.Errorf("migrate v0.26: %w", err)
	}
	if err := migrateV027(d); err != nil {
		return fmt.Errorf("migrate v0.27: %w", err)
	}
	if err := migrateV028(d); err != nil {
		return fmt.Errorf("migrate v0.28: %w", err)
	}
	return nil
}
