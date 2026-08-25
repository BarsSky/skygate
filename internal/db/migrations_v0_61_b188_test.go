// v1.5.2 (B188) — regression tests for migrateV061PG.
//
// The migration has two responsibilities:
//
//  1. Tag backfill. Every row in user_exit_node_prefs +
//     device_exit_node_prefs whose exit_node_tag matches
//     the legacy "tag:exit-<hostname>" form is rewritten
//     to the canonical "tag:dev-infra-<hostname>" value
//     looked up from node_owner_map. Rows whose hostname
//     doesn't resolve (e.g. the device was deleted from
//     headscale but the pref row is still around) are
//     LEFT ALONE — the operator can clean them up by hand.
//
//  2. Re-enable via pinning. Every pre-existing row that
//     has a real headscale tag (tag:dev-infra-*) AND
//     via_enabled=0 gets via_enabled flipped to 1. This
//     re-runs the v0.28.5 backfill that the
//     `freshlyAdded` guard in migrateV047PG silently
//     skipped on production (the column pre-existed).
//
// These tests pin both contracts plus the idempotency
// guarantee (re-running the migration is a no-op).
//
// Runs on a live PG instance via openTestDB (skipped when
// SKYGATE_TEST_PG_DSN is unset).

package db

import (
	"database/sql"
	"testing"
)

// b188SeedPortalUser inserts a single portal_users row
// so the FK on user_exit_node_prefs / device_exit_node_prefs
// is satisfied.
func b188SeedPortalUser(t *testing.T, d *sql.DB, id int64, username string) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES ($1, $2, 'x', 0, $3) ON CONFLICT (id) DO NOTHING`,
		id, username, ThemeVercel,
	); err != nil {
		t.Fatalf("b188SeedPortalUser id=%d: %v", id, err)
	}
}

// b188UserExitTag returns the current exit_node_tag for
// a (user_id) row in user_exit_node_prefs. Helper for
// the post-migration assertions.
func b188UserExitTag(t *testing.T, d *sql.DB, userID int64) string {
	t.Helper()
	var tag string
	if err := d.QueryRow(
		`SELECT exit_node_tag FROM user_exit_node_prefs WHERE user_id = $1`,
		userID,
	).Scan(&tag); err != nil {
		t.Fatalf("b188UserExitTag user_id=%d: %v", userID, err)
	}
	return tag
}

// b188UserExitViaEnabled returns the current via_enabled
// for a (user_id) row in user_exit_node_prefs.
func b188UserExitViaEnabled(t *testing.T, d *sql.DB, userID int64) bool {
	t.Helper()
	var v int
	if err := d.QueryRow(
		`SELECT via_enabled FROM user_exit_node_prefs WHERE user_id = $1`,
		userID,
	).Scan(&v); err != nil {
		t.Fatalf("b188UserExitViaEnabled user_id=%d: %v", userID, err)
	}
	return v != 0
}

// b188DeviceExitTag returns the current exit_node_tag for
// a (user_id, device_hostname) row in device_exit_node_prefs.
func b188DeviceExitTag(t *testing.T, d *sql.DB, userID int64, hostname string) string {
	t.Helper()
	var tag string
	if err := d.QueryRow(
		`SELECT exit_node_tag FROM device_exit_node_prefs WHERE user_id = $1 AND device_hostname = $2`,
		userID, hostname,
	).Scan(&tag); err != nil {
		t.Fatalf("b188DeviceExitTag user=%d host=%s: %v", userID, hostname, err)
	}
	return tag
}

// b188DeviceExitViaEnabled returns the current via_enabled
// for a (user_id, device_hostname) row.
func b188DeviceExitViaEnabled(t *testing.T, d *sql.DB, userID int64, hostname string) bool {
	t.Helper()
	var v int
	if err := d.QueryRow(
		`SELECT via_enabled FROM device_exit_node_prefs WHERE user_id = $1 AND device_hostname = $2`,
		userID, hostname,
	).Scan(&v); err != nil {
		t.Fatalf("b188DeviceExitViaEnabled user=%d host=%s: %v", userID, hostname, err)
	}
	return v != 0
}

// TestMigrateV061PG_TagBackfill_UserPref — the
// canonical scenario from the live audit (2026-08-25):
// user 6 (michail) has user_exit_node_prefs.exit_node_tag
// = "tag:exit-emilia" with via_enabled=1. After the
// migration:
//   - tag is rewritten to "tag:dev-infra-emilia"
//   - via_enabled stays 1 (was already enabled, no change)
func TestMigrateV061PG_TagBackfill_UserPref(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6001, "michail")
	b188SeedNodeOwner(t, d, 60100, "emilia", "tag:dev-infra-emilia")
	if err := SetUserExitNodePref(d, 6001, "tag:exit-emilia", 1, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG: %v", err)
	}

	if got := b188UserExitTag(t, d, 6001); got != "tag:dev-infra-emilia" {
		t.Errorf("user pref tag = %q, want %q", got, "tag:dev-infra-emilia")
	}
	if !b188UserExitViaEnabled(t, d, 6001) {
		t.Errorf("user pref via_enabled flipped to false; should be preserved")
	}
}

// TestMigrateV061PG_TagBackfill_DevicePref — the
// exact bug from the live operator report. user 6
// device "basic" has device_exit_node_prefs.exit_node_tag
// = "tag:exit-emilia" with via_enabled=0. After the
// migration:
//   - tag is rewritten to "tag:dev-infra-emilia"
//   - via_enabled is flipped to 1 (the v0.28.5 backfill
//     that the original migrateV047PG missed)
func TestMigrateV061PG_TagBackfill_DevicePref(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6002, "michail2")
	b188SeedNodeOwner(t, d, 60101, "emilia", "tag:dev-infra-emilia")
	if err := SetDeviceExitNodePref(d, 6002, "basic", "tag:exit-emilia", 6002, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG: %v", err)
	}

	if got := b188DeviceExitTag(t, d, 6002, "basic"); got != "tag:dev-infra-emilia" {
		t.Errorf("device pref tag = %q, want %q", got, "tag:dev-infra-emilia")
	}
	if !b188DeviceExitViaEnabled(t, d, 6002, "basic") {
		t.Errorf("device pref via_enabled=false after migration; want true (B188 re-enable)")
	}
}

// TestMigrateV061PG_ViaBackfill_OnlyForRealTags — the
// migration must NOT flip via_enabled to 1 for rows
// pointing at a ghost tag (e.g. tag:exit-emilia that
// couldn't be resolved because the node doesn't exist
// in node_owner_map). Such a row stays via_enabled=0
// because headscale would reject a via=[<ghost>] grant
// anyway.
func TestMigrateV061PG_ViaBackfill_OnlyForRealTags(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6003, "loneuser")
	// Intentionally NO node_owner_map row for "ghost-device"
	// — the tag can't be resolved.
	if err := SetDeviceExitNodePref(d, 6003, "ghost-device", "tag:exit-ghost-device", 6003, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG: %v", err)
	}

	// The tag should still be the legacy form (unresolved).
	if got := b188DeviceExitTag(t, d, 6003, "ghost-device"); got != "tag:exit-ghost-device" {
		t.Errorf("unresolved tag should be left alone, got %q", got)
	}
	// via_enabled should still be 0 (we don't pin to a ghost tag).
	if b188DeviceExitViaEnabled(t, d, 6003, "ghost-device") {
		t.Errorf("via_enabled should stay false for ghost-tag row")
	}
}

// TestMigrateV061PG_AlreadyEnabledNoOp — if a row
// already has the canonical tag AND via_enabled=1,
// the migration is a no-op (idempotency).
func TestMigrateV061PG_AlreadyEnabledNoOp(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6004, "preexisting")
	b188SeedNodeOwner(t, d, 60102, "karolina", "tag:dev-infra-karolina")
	if err := SetDeviceExitNodePref(d, 6004, "phone", "tag:dev-infra-karolina", 6004, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG: %v", err)
	}

	if got := b188DeviceExitTag(t, d, 6004, "phone"); got != "tag:dev-infra-karolina" {
		t.Errorf("tag should stay %q, got %q", "tag:dev-infra-karolina", got)
	}
	if !b188DeviceExitViaEnabled(t, d, 6004, "phone") {
		t.Errorf("via_enabled should stay true")
	}
}

// TestMigrateV061PG_Idempotent — re-running the
// migration on already-migrated data is a no-op. The
// LIKE 'tag:exit-%' WHERE clause matches nothing on
// the second pass; the via_enabled=1 UPDATE is
// idempotent.
func TestMigrateV061PG_Idempotent(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6005, "idem")
	b188SeedNodeOwner(t, d, 60103, "sharlotta", "tag:dev-infra-sharlotta")
	if err := SetDeviceExitNodePref(d, 6005, "laptop", "tag:exit-sharlotta", 6005, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First pass: rewrite + flip.
	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG first: %v", err)
	}
	first := b188DeviceExitTag(t, d, 6005, "laptop")
	if first != "tag:dev-infra-sharlotta" {
		t.Fatalf("after first pass: tag = %q, want %q", first, "tag:dev-infra-sharlotta")
	}

	// Second pass: no change.
	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG second: %v", err)
	}
	second := b188DeviceExitTag(t, d, 6005, "laptop")
	if second != first {
		t.Errorf("after second pass: tag changed from %q to %q", first, second)
	}
	if !b188DeviceExitViaEnabled(t, d, 6005, "laptop") {
		t.Errorf("via_enabled should still be true after second pass")
	}
}

// TestMigrateV061PG_MultipleHosts — the migration
// handles multiple distinct hostnames in a single
// transaction (one UPDATE statement covers all rows).
func TestMigrateV061PG_MultipleHosts(t *testing.T) {
	d := openTestDB(t)
	b188SeedPortalUser(t, d, 6006, "multi")
	b188SeedNodeOwner(t, d, 60110, "emilia", "tag:dev-infra-emilia")
	b188SeedNodeOwner(t, d, 60111, "karolina", "tag:dev-infra-karolina")
	if err := SetDeviceExitNodePref(d, 6006, "dev1", "tag:exit-emilia", 6006, false); err != nil {
		t.Fatalf("seed dev1: %v", err)
	}
	if err := SetDeviceExitNodePref(d, 6006, "dev2", "tag:exit-karolina", 6006, false); err != nil {
		t.Fatalf("seed dev2: %v", err)
	}

	if err := migrateV061PG(d); err != nil {
		t.Fatalf("migrateV061PG: %v", err)
	}

	if got := b188DeviceExitTag(t, d, 6006, "dev1"); got != "tag:dev-infra-emilia" {
		t.Errorf("dev1 tag = %q, want %q", got, "tag:dev-infra-emilia")
	}
	if got := b188DeviceExitTag(t, d, 6006, "dev2"); got != "tag:dev-infra-karolina" {
		t.Errorf("dev2 tag = %q, want %q", got, "tag:dev-infra-karolina")
	}
	// Both should have via_enabled flipped to 1.
	if !b188DeviceExitViaEnabled(t, d, 6006, "dev1") {
		t.Errorf("dev1 via_enabled should be true")
	}
	if !b188DeviceExitViaEnabled(t, d, 6006, "dev2") {
		t.Errorf("dev2 via_enabled should be true")
	}
}
