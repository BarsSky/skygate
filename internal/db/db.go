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
	migrateV022(d)
	migrateV023(d)
	if err := migrateV021(d); err != nil {
		return fmt.Errorf("migrate v0.21: %w", err)
	}
	if err := migrateV020(d); err != nil {
		return fmt.Errorf("migrate v0.20: %w", err)
	}
	return nil
}
