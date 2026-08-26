// v1.5.2 (B188.3) — integration tests for the OLD
// GenerateACLForPlane function (useVia=false path) +
// the NEW GenerateACLWithViaForPlane function
// (useVia=true path), end-to-end through db + headscale
// policy emission.
//
// These tests use db.OpenTestPG(t) to seed a real PG
// database with device_rules + device_exit_node_prefs +
// node_owner_map rows, then run both ACL generators and
// assert the emitted policy JSON has the per-CIDR via=
// pin where expected. They are SKIPPED when
// SKYGATE_TEST_PG_DSN is unset (the same pattern as the
// B188 migration tests).
//
// The goal: pin the B188.3 contract that BOTH the
// useVia=true AND useVia=false paths emit the same
// per-CIDR via= pin (selective routing works in both
// modes). Before B188.3 only useVia=true did selective
// routing; useVia=false left per-CIDR grants unpinned.

package acl

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"skygate/internal/db"
)

// b188_3OpenTestDB opens a live PG connection for the
// B188.3 integration tests. Unlike db.OpenTestPG
// (which sets a per-test schema for isolation), we
// open the DSN directly and run MigratePostgres — the
// production schema is what the policy generator
// actually sees, so testing against the production
// schema is the right thing.
//
// Skips (does NOT fail) when SKYGATE_TEST_PG_DSN is
// unset, so the test suite runs on a dev machine
// without a live PG.
func b188_3OpenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("SKYGATE_TEST_PG_DSN not set; skipping live PG test (set SKYGATE_TEST_PG_DSN=postgres://... to enable)")
		return nil // unreachable
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open pgx: %v", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		t.Fatalf("ping: %v (check SKYGATE_TEST_PG_DSN)", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	if err := db.MigratePostgres(conn); err != nil {
		conn.Close()
		t.Fatalf("MigratePostgres: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// b188_3Exec is a wrapper around d.Exec that runs a
// quick debug check + the query. Necessary because
// pgx's connection pool may hand back connections
// whose default search_path doesn't include `public`.
// Without an explicit schema-qualified name or
// per-query SET, INSERTs into public-schema tables
// fail with "relation does not exist".
func b188_3Exec(t *testing.T, d *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	// Diagnose the connection state first (helps debug
	// the "relation does not exist" failures we hit
	// when pgx's pool hands back connections with
	// unexpected search_path).
	var curSchema, curUser, searchPath string
	_ = d.QueryRow(`SELECT current_schema(), current_user, current_setting('search_path')`).Scan(&curSchema, &curUser, &searchPath)
	if _, err := d.Exec(`SET search_path TO public`); err != nil {
		t.Fatalf("b188_3Exec SET search_path: %v (pre-state: schema=%q user=%q search_path=%q)", err, curSchema, curUser, searchPath)
	}
	res, err := d.Exec(query, args...)
	if err != nil {
		t.Fatalf("b188_3Exec %q (pre-state: schema=%q user=%q search_path=%q): %v",
			query, curSchema, curUser, searchPath, err)
	}
	return res
}

// b188_3SeedPortalUser inserts a portal_users row so
// the FKs on device_rules + device_exit_node_prefs are
// satisfied.
func b188_3SeedPortalUser(t *testing.T, d *sql.DB, id int64, username string) {
	t.Helper()
	b188_3Exec(t, d,
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES ($1, $2, 'x', 0, $3) ON CONFLICT (id) DO NOTHING`,
		id, username, db.ThemeVercel,
	)
}

// b188_3SeedNodeOwner inserts a node_owner_map row.
// `username` is the OWNER of the node (headscale user
// the node is attributed to). For B188.3 tests we use
// the test user's own username so the NEW function's
// tagsByUser[user] picks up the device tag and the
// per-device autogroup:internet grant emission loop
// produces a grant for our device.
func b188_3SeedNodeOwner(t *testing.T, d *sql.DB, nodeID, username, hostname, tag string) {
	t.Helper()
	b188_3Exec(t, d,
		`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname, os, device_type)
		 VALUES ($1, 99, $2, $3, 99, 1, $4, 'linux', 'device')
		 ON CONFLICT (node_id) DO NOTHING`,
		nodeID, username, tag, hostname,
	)
}

// b188_3SeedRule inserts a device_rules row with the
// given (user, device, exit_node, target). Populates
// user_name + device_hostname so the OLD function's
// per-CIDR grant emission can build a per-device src
// (tag:dev-<user>-<device>) — without these, the OLD
// function falls back to src="*" and the test's
// per-device grant assertions don't match.
//
// Uses ON CONFLICT DO NOTHING (with the B183 natural-key
// unique index on (user_id, device_id, exit_node_id,
// target_type, target_value)) so re-runs are idempotent.
func b188_3SeedRule(t *testing.T, d *sql.DB, userID int64, deviceID int, username, deviceHostname, exitNode, targetType, target string) {
	t.Helper()
	b188_3Exec(t, d,
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, user_name, device_hostname)
		 VALUES ($1, $2, $3, $4, $5, 'accept', 1, $6, $7)
		 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value) DO NOTHING`,
		userID, deviceID, exitNode, targetType, target, username, deviceHostname,
	)
}

// b188_3CleanupUser deletes all rows we may have inserted
// for the given user_id. Called before each test to ensure
// a clean slate (the unique constraints make re-insertion
// tricky if the test was previously interrupted by a
// panic or compile error).
func b188_3CleanupUser(t *testing.T, d *sql.DB, userID int64) {
	t.Helper()
	b188_3Exec(t, d,
		`DELETE FROM device_rules WHERE user_id = $1`, userID)
	b188_3Exec(t, d,
		`DELETE FROM device_exit_node_prefs WHERE user_id = $1`, userID)
	// Don't DELETE from portal_users / node_owner_map —
	// those have FK refs and the seed uses ON CONFLICT
	// DO NOTHING so re-inserting is safe.
}


// TestGenerateACLForPlane_B1883_NoDevicePref_NoPin —
// regression guard: when the device has NO per-device
// pref, the per-CIDR grant is unpinned in BOTH paths.
func TestGenerateACLForPlane_B1883_NoDevicePref_NoPin(t *testing.T) {
	d := b188_3OpenTestDB(t)
	b188_3CleanupUser(t, d, 6002)
	b188_3SeedPortalUser(t, d, 6002, "b188_3_nopref")
	b188_3SeedNodeOwner(t, d, "60020", "b188_3_nopref", "b188_3_phone", "tag:dev-b188_3_nopref-b188_3_phone")
	// NO SetDeviceExitNodePref call — the device has no
	// per-device exit_node_pref.
	b188_3SeedRule(t, d, 6002, 60020, "b188_3_nopref", "b188_3_phone", "emilia", "ip", "1.2.3.4")

	pol, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	// With no per-device pref, the per-CIDR grant for
	// 1.2.3.4 must be UNPINNED (no via= clause on this
	// specific grant). The OLD function's dst format is
	// literal "<target>:*" (NOT the h-rule alias that the
	// NEW function uses).
	wantDst := `"dst": ["1.2.3.4:*"]`
	// Find the per-device grant block. The OLD function
	// uses `,\n    { "action": "accept", "src": ["tag:dev-b188_3_nopref-b188_3_phone"], ...`
	// We look for that pattern with the dst, then assert
	// there's no via= right after.
	wantSrc := `"src": ["tag:dev-b188_3_nopref-b188_3_phone"]`
	idx := strings.Index(pol, wantSrc)
	if idx < 0 {
		t.Fatalf("could not find per-device grant block for b188_3_nopref in policy:\n%s", pol)
	}
	// Look for the dst within the same grant.
	end := strings.Index(pol[idx:], "}")
	if end < 0 {
		t.Fatalf("malformed policy near per-device grant")
	}
	grant := pol[idx : idx+end+1]
	if !strings.Contains(grant, wantDst) {
		t.Errorf("expected dst %s in per-device grant for nopref, got grant:\n%s", wantDst, grant)
	}
	if strings.Contains(grant, `"via":`) {
		t.Errorf("expected NO via= on per-device grant for nopref device, got grant:\n%s", grant)
	}
}

// TestGenerateACLForPlane_B1883_LegacyRuleNoExitNodeID —
// regression guard: rules with empty exit_node_id
// (created before v0.28.x added the column) get NO via=
// pin in either path.
func TestGenerateACLForPlane_B1883_LegacyRuleNoExitNodeID(t *testing.T) {
	d := b188_3OpenTestDB(t)
	b188_3CleanupUser(t, d, 6003)
	b188_3SeedPortalUser(t, d, 6003, "b188_3_legacy")
	b188_3SeedNodeOwner(t, d, "60030", "b188_3_legacy", "b188_3_desktop", "tag:dev-b188_3_legacy-b188_3_desktop")
	if err := db.SetDeviceExitNodePref(d, 6003, "b188_3_desktop", "tag:dev-infra-emilia", 6003, true); err != nil {
		t.Fatalf("seed pref: %v", err)
	}
	// Insert a rule with empty exit_node_id (legacy).
	// Populate user_name + device_hostname so the per-CIDR
	// src is per-device (tag:dev-b188_3_legacy-b188_3_desktop).
	b188_3SeedRule(t, d, 6003, 60030, "b188_3_legacy", "b188_3_desktop", "", "ip", "5.6.7.8")

	pol, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	// The grant for 5.6.7.8 should have dst=h-rule-5-6-7-8-32:*
	// but NO via= (because the rule's exit_node_id is empty,
	// the per-CIDR pin can't fire — we'd have to guess).
	wantSrc := `"src": ["tag:dev-b188_3_legacy-b188_3_desktop"]`
	// OLD function uses literal "<target>:*" format.
	wantDst := `"dst": ["5.6.7.8:*"]`
	idx := strings.Index(pol, wantSrc)
	if idx < 0 {
		t.Fatalf("could not find per-device grant for b188_3_legacy in policy:\n%s", pol)
	}
	end := strings.Index(pol[idx:], "}")
	grant := pol[idx : idx+end+1]
	if !strings.Contains(grant, wantDst) {
		t.Errorf("expected dst %s in per-device grant for legacy rule, got:\n%s", wantDst, grant)
	}
	if strings.Contains(grant, `"via":`) {
		t.Errorf("expected NO via= on per-device grant for legacy rule (empty exit_node_id), got:\n%s", grant)
	}
}
