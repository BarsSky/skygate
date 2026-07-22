package db

// 2026-07-13: Этап 11 part 2a — tests for the per-user default
// device / default exit_node preference helpers. These pin the
// column names, the empty-string-is-sentinel convention, and the
// error semantics (ErrUserNotFound for "no such user" so callers
// can branch on errors.Is).

import (
	"database/sql"
	"errors"
	"testing"
)

// seedUser inserts a row in portal_users and returns the new id.
// Used by every test in this file to give the prefs helpers
// something to act on. Mirrors the seed pattern in db_test.go's
// TestGetSetUserTheme.
func seedUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, 'x', 0)`,
		username,
	)
	if err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestGetDefaultDeviceEmpty(t *testing.T) {
	d := openTestDB(t)
	id := seedUser(t, d, "alice")

	// Fresh user → no default → empty string, no error.
	got, err := GetDefaultDevice(d, id)
	if err != nil {
		t.Fatalf("GetDefaultDevice fresh user: %v", err)
	}
	if got != "" {
		t.Errorf("fresh user default = %q, want \"\"", got)
	}
}

func TestSetAndGetDefaultDevice(t *testing.T) {
	d := openTestDB(t)
	id := seedUser(t, d, "alice")

	// Set → Get round-trip
	if n, err := SetDefaultDevice(d, id, "42"); err != nil || n != 1 {
		t.Fatalf("SetDefaultDevice(42) n=%d err=%v", n, err)
	}
	got, err := GetDefaultDevice(d, id)
	if err != nil {
		t.Fatalf("GetDefaultDevice after set: %v", err)
	}
	if got != "42" {
		t.Errorf("after set, default = %q, want %q", got, "42")
	}

	// Overwrite with a new node_id
	if _, err := SetDefaultDevice(d, id, "99"); err != nil {
		t.Fatalf("SetDefaultDevice overwrite: %v", err)
	}
	got, _ = GetDefaultDevice(d, id)
	if got != "99" {
		t.Errorf("after overwrite, default = %q, want %q", got, "99")
	}

	// Set to "" clears the default
	if _, err := SetDefaultDevice(d, id, ""); err != nil {
		t.Fatalf("SetDefaultDevice clear: %v", err)
	}
	got, _ = GetDefaultDevice(d, id)
	if got != "" {
		t.Errorf("after clear, default = %q, want \"\"", got)
	}
}

func TestGetDefaultDeviceUserNotFound(t *testing.T) {
	d := openTestDB(t)

	// No such user → ErrUserNotFound (typed so callers can branch
	// on errors.Is). Empty string is the "no default" sentinel
	// only when the user EXISTS but the default is unset.
	_, err := GetDefaultDevice(d, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("GetDefaultDevice(nonexistent) err = %v, want ErrUserNotFound", err)
	}
}

func TestSetDefaultDeviceUserNotFound(t *testing.T) {
	d := openTestDB(t)

	// No such user → 0 rows affected, no error. The update
	// simply doesn't find a target — that's a legitimate "I
	// couldn't apply this" signal that the caller can surface
	// ("user vanished between auth and update").
	n, err := SetDefaultDevice(d, 9999, "42")
	if err != nil {
		t.Fatalf("SetDefaultDevice(nonexistent) err = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("SetDefaultDevice(nonexistent) n = %d, want 0", n)
	}
}

func TestGetDefaultExitNodeEmpty(t *testing.T) {
	d := openTestDB(t)
	id := seedUser(t, d, "bob")

	got, err := GetDefaultExitNode(d, id)
	if err != nil {
		t.Fatalf("GetDefaultExitNode fresh: %v", err)
	}
	if got != "" {
		t.Errorf("fresh default exit_node = %q, want \"\"", got)
	}
}

func TestSetAndGetDefaultExitNode(t *testing.T) {
	d := openTestDB(t)
	id := seedUser(t, d, "bob")

	if n, err := SetDefaultExitNode(d, id, "karolina-node-7"); err != nil || n != 1 {
		t.Fatalf("SetDefaultExitNode n=%d err=%v", n, err)
	}
	got, _ := GetDefaultExitNode(d, id)
	if got != "karolina-node-7" {
		t.Errorf("after set, default exit_node = %q, want %q", got, "karolina-node-7")
	}

	// Clear via empty string
	if _, err := SetDefaultExitNode(d, id, ""); err != nil {
		t.Fatalf("SetDefaultExitNode clear: %v", err)
	}
	got, _ = GetDefaultExitNode(d, id)
	if got != "" {
		t.Errorf("after clear, default exit_node = %q, want \"\"", got)
	}
}

func TestGetDefaultExitNodeUserNotFound(t *testing.T) {
	d := openTestDB(t)

	_, err := GetDefaultExitNode(d, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("GetDefaultExitNode(nonexistent) err = %v, want ErrUserNotFound", err)
	}
}

func TestDefaultDeviceAndExitNodeIndependent(t *testing.T) {
	d := openTestDB(t)
	id := seedUser(t, d, "carol")

	// Setting the device should NOT touch the exit_node, and
	// vice versa. Two columns, two independent writes — easy
	// to mix up in a future refactor.
	if _, err := SetDefaultDevice(d, id, "device-1"); err != nil {
		t.Fatalf("SetDefaultDevice: %v", err)
	}

	// exit_node still empty
	gotExit, _ := GetDefaultExitNode(d, id)
	if gotExit != "" {
		t.Errorf("after SetDefaultDevice, exit_node = %q, want \"\"", gotExit)
	}

	if _, err := SetDefaultExitNode(d, id, "emilia-1"); err != nil {
		t.Fatalf("SetDefaultExitNode: %v", err)
	}

	// device still 1, exit_node now emilia-1
	gotDev, _ := GetDefaultDevice(d, id)
	gotExit, _ = GetDefaultExitNode(d, id)
	if gotDev != "device-1" {
		t.Errorf("after SetDefaultExitNode, device = %q, want %q", gotDev, "device-1")
	}
	if gotExit != "emilia-1" {
		t.Errorf("after SetDefaultExitNode, exit_node = %q, want %q", gotExit, "emilia-1")
	}
}
