package telegram

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	// Open a private in-memory DB and create the minimal tables
	// commands_test.go needs (device_rules, portal_users, acl_snapshots).
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE device_rules (id INTEGER PRIMARY KEY, target_value TEXT)`,
		`CREATE TABLE portal_users (id INTEGER PRIMARY KEY, username TEXT)`,
		`CREATE TABLE acl_snapshots (id INTEGER PRIMARY KEY, version INTEGER, applied_success INTEGER)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	// Seed a few rows so the reply has substance.
	for i := 0; i < 12; i++ {
		_, _ = d.Exec(`INSERT INTO device_rules(target_value) VALUES (?)`, "x")
	}
	_, _ = d.Exec(`INSERT INTO portal_users(username) VALUES ('skyadmin')`)
	_, _ = d.Exec(`INSERT INTO acl_snapshots(version, applied_success) VALUES (5, 1)`)
	t.Cleanup(func() { _ = d.Close(); _ = filepath.Clean("") })
	return d
}

func TestHandleCommandStatus(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), d, "/status")
	if !strings.Contains(got, "rules: 12") {
		t.Errorf("expected rules count, got: %q", got)
	}
	if !strings.Contains(got, "users: 1") {
		t.Errorf("expected users count, got: %q", got)
	}
	if !strings.Contains(got, "last acl: #5") {
		t.Errorf("expected last acl, got: %q", got)
	}
}

func TestHandleCommandHelp(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), d, "/help")
	if !strings.Contains(got, "/status") {
		t.Errorf("expected /status in /help, got: %q", got)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), d, "/foobar")
	if !strings.Contains(got, "Unknown") {
		t.Errorf("expected unknown message, got: %q", got)
	}
}

func TestHandleCommandCaseInsensitive(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), d, "/STATUS")
	if !strings.Contains(got, "rules:") {
		t.Errorf("expected status body, got: %q", got)
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), d, "")
	if !strings.Contains(got, "Empty") {
		t.Errorf("expected empty message, got: %q", got)
	}
}
