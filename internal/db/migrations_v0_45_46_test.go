// internal/db/migrations_v0_45_46_test.go
//
// v0.33.1.27 — regression test for SetUserExitNodePref +
// SetDeviceExitNodePref.
//
// The v0.33.1.19 fix tried to put nowUnixSQL() at the
// right column position by using
// `placeholdersList(3) + placeholdersList(1)` (or
// `placeholdersList(4) + placeholdersList(1)`) in the SQL.
// But placeholdersList always starts the count at 1, so
// concatenating two of them produces TWO references to
// `$1` in the same query:
//
//   `placeholdersList(3) + placeholdersList(1)`
//   = "$1, $2, $3" + "$1" = "$1, $2, $3, $1"
//
// pgx rejected the query with "mismatched param and
// argument count" (3 unique $N placeholders vs 4 Go args
// for SetUserExitNodePref). The /my/exit-nodes +
// /my/devices/preferred-exit POST handlers returned 500
// on every click for every user. The fix is
// PlaceholdersRange(from, to) which generates a
// contiguous range of $N placeholders starting at `from`.
//
// The tests below pin the round-trip: write a value, read
// it back, assert the columns are correctly populated
// (updated_at is a real timestamp, via_enabled is 0 or 1).
// A regression of the v0.33.1.19 bug would either fail
// with a 500-style error at the SetUserExitNodePref call,
// OR silently write updated_at=0 and via_enabled=<unix
// timestamp> (which is always truthy, so the via_enabled
// check would still pass — but updated_at would be 0,
// which is the testable side of the bug).
//
// Runs on SQLite (the default build). The PG-specific
// PlaceholdersRange format check is in
// placeholders_range_pg_test.go (build-tagged for
// -tags postgres).

package db

import (
	"testing"
	"time"
)

// TestSetUserExitNodePref_RoundTrip pins the v0.33.1.27 fix
// for the per-user exit-node pref. Pre-fix, the
// placeholdersList concatenation produced "$1, $2, $3, $1"
// on PG which the driver rejected with "mismatched param
// and argument count". The Set call returned an error and
// /my/exit-nodes "Set as my preferred" returned 500. Post-
// fix, Set succeeds and the row is correctly written
// (updated_at is a real Unix timestamp, via_enabled
// matches the input bool).
func TestSetUserExitNodePref_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	// Seed a portal_user (the prefs FK references this).
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly (V054
	// reserves id=99 for the infra user, so AUTOINCREMENT
	// no longer starts at 1).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'skyadmin', 'x', 1, ?)`,
		ThemeVercel,
	); err != nil {
		t.Fatalf("seed portal_user: %v", err)
	}

	// First set: tag:exit-emilia, via=false.
	if err := SetUserExitNodePref(d, 1, "tag:exit-emilia", 1, false); err != nil {
		t.Fatalf("first set: %v", err)
	}

	// Read back.
	got, err := GetUserExitNodePref(d, 1)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got.ExitNodeTag != "tag:exit-emilia" {
		t.Errorf("tag = %q, want %q", got.ExitNodeTag, "tag:exit-emilia")
	}
	if got.ViaEnabled {
		t.Errorf("via_enabled = true, want false")
	}
	// Pre-fix bug: updated_at would be 0 or 1 (because
	// viaInt was written into the updated_at column slot).
	// Real Unix timestamps are > 1.7e9 (year 2024+).
	if got.UpdatedAt <= 1 {
		t.Errorf("updated_at = %d, looks like the v0.33.1.19 bug (viaInt leaked into updated_at)", got.UpdatedAt)
	}

	// Upsert with viaEnabled=true.
	if err := SetUserExitNodePref(d, 1, "tag:exit-karolina", 1, true); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = GetUserExitNodePref(d, 1)
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got.ExitNodeTag != "tag:exit-karolina" {
		t.Errorf("upsert tag = %q, want %q", got.ExitNodeTag, "tag:exit-karolina")
	}
	if !got.ViaEnabled {
		t.Errorf("via_enabled = false, want true")
	}
	if got.UpdatedAt <= 1 {
		t.Errorf("updated_at = %d, looks like the v0.33.1.19 bug", got.UpdatedAt)
	}

	// Clear (empty tag) → DELETE; the row should be gone.
	if err := SetUserExitNodePref(d, 1, "", 1, false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = GetUserExitNodePref(d, 1)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.ExitNodeTag != "" {
		t.Errorf("after clear: tag = %q, want empty", got.ExitNodeTag)
	}
}

// TestSetDeviceExitNodePref_RoundTrip pins the v0.33.1.27
// fix for the per-device exit-node pref. Same shape as
// the per-user test above: write, read, assert the columns
// aren't swapped.
func TestSetDeviceExitNodePref_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'skyadmin', 'x', 1, ?)`,
		ThemeVercel,
	); err != nil {
		t.Fatalf("seed portal_user: %v", err)
	}

	// Per-device pref for (user 1, hostname "skybars").
	if err := SetDeviceExitNodePref(d, 1, "skybars", "tag:exit-emilia", 1, false); err != nil {
		t.Fatalf("first set: %v", err)
	}

	// Read back.
	got, err := GetDeviceExitNodePref(d, 1, "skybars")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got.ExitNodeTag != "tag:exit-emilia" {
		t.Errorf("tag = %q, want %q", got.ExitNodeTag, "tag:exit-emilia")
	}
	if got.ViaEnabled {
		t.Errorf("via_enabled = true, want false")
	}
	if got.UpdatedAt <= 1 {
		t.Errorf("updated_at = %d, looks like the v0.33.1.19 bug (viaInt leaked into updated_at)", got.UpdatedAt)
	}

	// Upsert with a different tag + viaEnabled=true.
	if err := SetDeviceExitNodePref(d, 1, "skybars", "tag:exit-karolina", 1, true); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = GetDeviceExitNodePref(d, 1, "skybars")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got.ExitNodeTag != "tag:exit-karolina" {
		t.Errorf("upsert tag = %q, want %q", got.ExitNodeTag, "tag:exit-karolina")
	}
	if !got.ViaEnabled {
		t.Errorf("via_enabled = false, want true")
	}
	if got.UpdatedAt <= 1 {
		t.Errorf("updated_at = %d, looks like the v0.33.1.19 bug", got.UpdatedAt)
	}

	// Clear (empty tag) → DELETE; the row should be gone.
	if err := SetDeviceExitNodePref(d, 1, "skybars", "", 1, false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = GetDeviceExitNodePref(d, 1, "skybars")
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.ExitNodeTag != "" {
		t.Errorf("after clear: tag = %q, want empty", got.ExitNodeTag)
	}
}

// TestSetUserExitNodePref_RecentTimestamp pins the time
// component of the fix. The function should set updated_at
// to a recent Unix timestamp (within the last minute), not
// 0 or 1. This catches the v0.33.1.19 bug where viaInt (0
// or 1) leaked into the updated_at column.
func TestSetUserExitNodePref_RecentTimestamp(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'skyadmin', 'x', 1, ?)`,
		ThemeVercel,
	); err != nil {
		t.Fatalf("seed portal_user: %v", err)
	}
	before := time.Now().UTC().Add(-1 * time.Minute).Unix()
	if err := SetUserExitNodePref(d, 1, "tag:exit-emilia", 1, false); err != nil {
		t.Fatalf("set: %v", err)
	}
	after := time.Now().UTC().Add(1 * time.Minute).Unix()
	got, err := GetUserExitNodePref(d, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Errorf("updated_at = %d, want in [%d, %d] (between 1m ago and 1m from now)",
			got.UpdatedAt, before, after)
	}
}

