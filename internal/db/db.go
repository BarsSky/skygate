package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	ThemeLinear = "linear"
	ThemeVercel = "vercel"
	ThemeSentry = "sentry"
)

func IsValidTheme(t string) bool {
	switch t {
	case ThemeLinear, ThemeVercel, ThemeSentry:
		return true
	}
	return false
}

type User struct {
	ID              int64
	Username        string
	PasswordHash    string
	IsAdmin         bool
	HeadscaleUserID int64
	CreatedAt       time.Time
	Theme           string
}

type Device struct {
	ID          int64
	UserID      int64
	NodeID      int64
	Hostname    string
	TailscaleIP string
	LastSeen    time.Time
	CreatedAt   time.Time
}

type AuditEntry struct {
	ID        int64
	UserID    int64
	Username  string
	Action    string
	Detail    string
	CreatedAt time.Time
}

func Open(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err := d.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := migrate(d); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS portal_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		headscale_user_id INTEGER,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);
	CREATE TABLE IF NOT EXISTS devices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		node_id INTEGER,
		hostname TEXT,
		last_seen INTEGER,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		FOREIGN KEY(user_id) REFERENCES portal_users(id)
	);
	CREATE TABLE IF NOT EXISTS preauth_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		key TEXT NOT NULL,
		used INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		FOREIGN KEY(user_id) REFERENCES portal_users(id)
	);
	CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		username TEXT,
		action TEXT NOT NULL,
		detail TEXT,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);

	-- headscale moves a node to the synthetic "tagged-devices" user whenever
	-- a tag is applied. To remember the real owner, skygate records the
	-- mapping when an admin marks a node public (or any other tag). When the
	-- tag is removed the row is deleted.
	CREATE TABLE IF NOT EXISTS node_owner_map (
		node_id INTEGER PRIMARY KEY,
		headscale_user_id INTEGER NOT NULL,
		username TEXT NOT NULL,
		tag TEXT NOT NULL,
		tagged_by_user_id INTEGER,
		tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	);

	CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id);
	CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
	CREATE INDEX IF NOT EXISTS idx_preauth_user ON preauth_keys(user_id);
	CREATE INDEX IF NOT EXISTS idx_node_owner_user ON node_owner_map(headscale_user_id);
	`
	if _, err := d.Exec(schema); err != nil {
		return err
	}

	// Idempotent migration: add theme column to portal_users if missing (PRAGMA doesn't have IF NOT EXISTS for columns)
	cols, _ := d.Query("PRAGMA table_info(portal_users)")
	defer cols.Close()
	hasTheme := false
	for cols.Next() {
		var cid, notnull int
		var name, ctype string
		var dflt, pk sql.NullString
		if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil {
			if name == "theme" {
				hasTheme = true
			}
		}
	}
	if !hasTheme {
		if _, err := d.Exec("ALTER TABLE portal_users ADD COLUMN theme TEXT NOT NULL DEFAULT 'linear'"); err != nil {
			return fmt.Errorf("add theme column: %w", err)
		}
	}
	return nil
}

func (u *User) CreatedAtStr() string {
	return time.Unix(u.CreatedAt.Unix(), 0).Format("2006-01-02 15:04")
}

func (u *User) EffectiveTheme() string {
	if u.Theme == "" || !IsValidTheme(u.Theme) {
		return ThemeLinear
	}
	return u.Theme
}

func ThemeLabel(theme string) string {
	switch theme {
	case ThemeLinear:
		return "Linear"
	case ThemeVercel:
		return "Vercel"
	case ThemeSentry:
		return "Sentry"
	}
	return "Linear"
}

// GetUserTheme reads the theme for the given user. Returns ThemeLinear if user/theme missing.
func GetUserTheme(d *sql.DB, userID int64) string {
	var theme sql.NullString
	err := d.QueryRow("SELECT theme FROM portal_users WHERE id = ?", userID).Scan(&theme)
	if err != nil || !theme.Valid {
		return ThemeLinear
	}
	if !IsValidTheme(theme.String) {
		return ThemeLinear
	}
	return theme.String
}

// SetUserTheme updates the user's theme preference.
func SetUserTheme(d *sql.DB, userID int64, theme string) error {
	if !IsValidTheme(theme) {
		return fmt.Errorf("invalid theme: %q", theme)
	}
	_, err := d.Exec("UPDATE portal_users SET theme = ? WHERE id = ?", theme, userID)
	return err
}
