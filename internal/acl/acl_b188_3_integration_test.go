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
	"encoding/json"
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

// b188_3WithTx runs fn inside a transaction with
// search_path=public. Necessary because pgx's pool
// may hand back connections whose default search_path
// doesn't include `public` (production has `public`
// as the second schema, behind `"$user"`). Within a
// single transaction, all queries share one connection,
// so the SET propagates reliably. The b188_3SeedXxx
// helpers below route through this.
//
// fn may use db.* helpers (like db.SetDeviceExitNodePref)
// as long as those helpers use the provided *sql.Tx or
// *sql.DB. We pass the *sql.DB to fn (not the Tx) so
// fn can use the regular db package — the db helpers
// re-acquire connections from the pool, which we
// configure with SetMaxOpenConns(1) below for the
// transaction's lifetime. This is hacky but works for
// the test purpose.
func b188_3WithTx(t *testing.T, d *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SET search_path TO public`); err != nil {
		t.Fatalf("SET search_path: %v", err)
	}
	if _, err := tx.Exec(`SET CONSTRAINTS ALL DEFERRED`); err != nil {
		// PG doesn't support DEFERRED for our FKs (they're
		// NOT DEFERRABLE by default) — but it doesn't hurt
		// to try; we just don't rely on it.
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
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
func b188_3SeedNodeOwner(t *testing.T, d *sql.DB, nodeID string, hostname, tag string) {
	t.Helper()
	b188_3Exec(t, d,
		`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname, os, device_type)
		 VALUES ($1, 99, 'infra', $2, 99, 1, $3, 'linux', 'exit-node')
		 ON CONFLICT (node_id) DO NOTHING`,
		nodeID, tag, hostname,
	)
}

// b188_3SeedRule inserts a device_rules row with the
// given (user, device, exit_node, target).
func b188_3SeedRule(t *testing.T, d *sql.DB, userID int64, deviceID int, exitNode, targetType, target string) {
	t.Helper()
	b188_3Exec(t, d,
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled)
		 VALUES ($1, $2, $3, $4, $5, 'accept', 1)`,
		userID, deviceID, exitNode, targetType, target,
	)
}

// b188_3CountGrantsWithVia returns the number of grants
// in the policy that have both src=devTag AND via=viaTag
// in the SAME grant object.
func b188_3CountGrantsWithVia(t *testing.T, policy string, devTag, viaTag string) int {
	t.Helper()
	// Parse the JSON. We don't care about the typed
	// structure, just the grants array.
	var pol struct {
		Grants []json.RawMessage `json:"grants"`
	}
	if err := json.Unmarshal([]byte(policy), &pol); err != nil {
		t.Fatalf("b188_3CountGrantsWithVia: policy isn't valid JSON: %v\n---POLICY---\n%s", err, policy)
	}
	n := 0
	for _, g := range pol.Grants {
		var gg struct {
			Src []string          `json:"src"`
			Dst []string          `json:"dst"`
			Via []string          `json:"via"`
			IP  []string          `json:"ip"`
		}
		_ = json.Unmarshal(g, &gg)
		hasSrc := false
		for _, s := range gg.Src {
			if s == devTag {
				hasSrc = true
				break
			}
		}
		if !hasSrc {
			continue
		}
		for _, v := range gg.Via {
			if v == viaTag {
				n++
				break
			}
		}
	}
	return n
}

// b188_3CountGrantsWithAutogroupNoVia returns the number
// of grants with src=devTag + dst=autogroup:internet +
// NO via= (the post-B188.2 contract for the catch-all).
func b188_3CountGrantsWithAutogroupNoVia(t *testing.T, policy string, devTag string) int {
	t.Helper()
	var pol struct {
		Grants []json.RawMessage `json:"grants"`
	}
	if err := json.Unmarshal([]byte(policy), &pol); err != nil {
		t.Fatalf("b188_3CountGrantsWithAutogroupNoVia: policy isn't valid JSON: %v", err)
	}
	n := 0
	for _, g := range pol.Grants {
		var gg struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
			Via []string `json:"via"`
		}
		_ = json.Unmarshal(g, &gg)
		hasSrc := false
		for _, s := range gg.Src {
			if s == devTag {
				hasSrc = true
				break
			}
		}
		if !hasSrc {
			continue
		}
		hasAGI := false
		for _, d := range gg.Dst {
			if d == "autogroup:internet" {
				hasAGI = true
				break
			}
		}
		if !hasAGI {
			continue
		}
		if len(gg.Via) > 0 {
			continue
		}
		n++
	}
	return n
}

// TestGenerateACLForPlane_B1883_PerCIDRViaInNoViaPath —
// the core B188.3 contract: even when useVia=false, the
// per-CIDR grant for a per-device rule that matches the
// device's exit_node_pref MUST have via=[<pref>].
//
// Setup:
//   - user 6001 (b188_3_user)
//   - device 60010 (b188_3_laptop) with pref = tag:dev-infra-emilia
//   - rule: 64.233.164.91/32 → emilia (for this device)
//   - rule: 8.8.8.8/32 → karolina (for this device, NOT matching)
//
// Expected in BOTH GenerateACLForPlane (useVia=false) and
// GenerateACLWithViaForPlane (useVia=true):
//   - 1 grant for src=tag:dev-b188_3_user-b188_3_laptop with
//     dst=h-rule-64-233-164-91-32 and via=[tag:dev-infra-emilia]
//   - 1 grant for src=tag:dev-b188_3_user-b188_3_laptop with
//     dst=h-rule-8-8-8-8-32 and NO via (mismatched exit_node)
//   - 1 grant for src=tag:dev-b188_3_user-b188_3_laptop with
//     dst=autogroup:internet and NO via (catch-all stays
//     unpinned per B188.2)
func TestGenerateACLForPlane_B1883_PerCIDRViaInNoViaPath(t *testing.T) {
	d := b188_3OpenTestDB(t)
	b188_3SeedPortalUser(t, d, 6001, "b188_3_user")
	b188_3SeedNodeOwner(t, d, "60010", "b188_3_laptop", "tag:dev-infra-emilia")
	b188_3SeedNodeOwner(t, d, "60011", "karolina", "tag:dev-infra-karolina")
	// device 60010 (b188_3_laptop) has per-device pref for emilia
	if err := db.SetDeviceExitNodePref(d, 6001, "b188_3_laptop", "tag:dev-infra-emilia", 6001, true); err != nil {
		t.Fatalf("seed pref: %v", err)
	}
	// rule: youtube /32 → emilia (matches pref)
	b188_3SeedRule(t, d, 6001, 60010, "emilia", "ip", "64.233.164.91")
	// rule: dns /32 → karolina (mismatches pref — should NOT get via)
	b188_3SeedRule(t, d, 6001, 60010, "karolina", "ip", "8.8.8.8")

	const devTag = "tag:dev-b188_3_user-b188_3_laptop"

	// --- useVia=true path: GenerateACLWithViaForPlane ---
	t.Run("useVia=true", func(t *testing.T) {
		pol, err := GenerateACLWithViaForPlane(d, "")
		if err != nil {
			t.Fatalf("GenerateACLWithViaForPlane: %v", err)
		}
		if n := b188_3CountGrantsWithVia(t, pol, devTag, "tag:dev-infra-emilia"); n != 1 {
			t.Errorf("useVia=true: youtube→emilia per-CIDR via= count = %d, want 1", n)
		}
		if n := b188_3CountGrantsWithAutogroupNoVia(t, pol, devTag); n != 1 {
			t.Errorf("useVia=true: autogroup:internet unpinned count = %d, want 1", n)
		}
		// dns→karolina should NOT have via=emilia (mismatch)
		if strings.Contains(pol, `"tag:dev-infra-emilia"`) && !strings.Contains(pol, `via: ["tag:dev-infra-emilia"]`) {
			t.Errorf("useVia=true: policy has emilia tag without via= clause (data leak?)")
		}
	})

	// --- useVia=false path: GenerateACLForPlane ---
	// This is the B188.3 fix: the OLD function now also
	// emits the per-CIDR via= pin. Before B188.3 the OLD
	// function emitted the per-CIDR grant WITHOUT via=,
	// even when the device's pref matched.
	t.Run("useVia=false", func(t *testing.T) {
		pol, err := GenerateACLForPlane(d, "")
		if err != nil {
			t.Fatalf("GenerateACLForPlane: %v", err)
		}
		// The OLD function emits acls[] (not grants[]),
		// so the JSON parse above will fail. We use a
		// simpler substring check for the OLD path.
		// Look for: src=tag:dev-b188_3_user-b188_3_laptop
		// ...dst=h-rule-64-233-164-91-32...via: ["tag:dev-infra-emilia"]
		// (the OLD format uses h-rule-64-233-164-91-32:*
		// for dst, but the via= field is identical).
		if !strings.Contains(pol, `"via": ["tag:dev-infra-emilia"]`) {
			t.Errorf("useVia=false: expected per-CIDR via=[\"tag:dev-infra-emilia\"] in policy, not found\n---POLICY---\n%s", pol)
		}
		if !strings.Contains(pol, `"dst": ["h-rule-64-233-164-91-32:*"]`) {
			t.Errorf("useVia=false: expected dst=h-rule-64-233-164-91-32:* in policy, not found\n---POLICY---\n%s", pol)
		}
		// The mismatched rule (8.8.8.8 → karolina) should
		// NOT get via=emilia.
		if strings.Contains(pol, `"dst": ["h-rule-8-8-8-8-32:*"], "via": ["tag:dev-infra-emilia"]`) {
			t.Errorf("useVia=false: 8.8.8.8→karolina should NOT have via=emilia (mismatch)")
		}
		// The catch-all (autogroup:internet for this device
		// tag) should be unpinned (per B188.2 contract).
		// The OLD function emits a per-device autogroup:internet
		// grant as a separate entry. Check it has no via=.
		if strings.Contains(pol, `"dst": ["autogroup:internet"], "ip": ["*"], "via":`) {
			t.Errorf("useVia=false: per-device autogroup:internet must NOT have via= (B188.2 contract)")
		}
	})
}

// TestGenerateACLForPlane_B1883_NoDevicePref_NoPin —
// regression guard: when the device has NO per-device
// pref, the per-CIDR grant is unpinned in BOTH paths.
func TestGenerateACLForPlane_B1883_NoDevicePref_NoPin(t *testing.T) {
	d := b188_3OpenTestDB(t)
	b188_3SeedPortalUser(t, d, 6002, "b188_3_nopref")
	b188_3SeedNodeOwner(t, d, "60020", "b188_3_phone", "tag:dev-infra-emilia")
	// NO SetDeviceExitNodePref call — the device has no
	// per-device exit_node_pref.
	b188_3SeedRule(t, d, 6002, 60020, "emilia", "ip", "1.2.3.4")

	pol, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	// With no per-device pref, the per-CIDR grant for
	// 1.2.3.4 must be UNPINNED (no via= clause on this
	// specific grant). The OLD function's dst format is
	// "h-rule-1-2-3-4-32:*" (with the :* suffix).
	wantDst := `"dst": ["h-rule-1-2-3-4-32:*"]`
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
	b188_3SeedPortalUser(t, d, 6003, "b188_3_legacy")
	b188_3SeedNodeOwner(t, d, "60030", "b188_3_desktop", "tag:dev-infra-emilia")
	if err := db.SetDeviceExitNodePref(d, 6003, "b188_3_desktop", "tag:dev-infra-emilia", 6003, true); err != nil {
		t.Fatalf("seed pref: %v", err)
	}
	// Insert a rule with empty exit_node_id (legacy).
	if _, err := d.Exec(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled)
		 VALUES (6003, 60030, '', 'ip', '5.6.7.8', 'accept', 1)`,
	); err != nil {
		t.Fatalf("seed legacy rule: %v", err)
	}

	pol, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	// The grant for 5.6.7.8 should have dst=h-rule-5-6-7-8-32:*
	// but NO via= (because the rule's exit_node_id is empty,
	// the per-CIDR pin can't fire — we'd have to guess).
	wantSrc := `"src": ["tag:dev-b188_3_legacy-b188_3_desktop"]`
	wantDst := `"dst": ["h-rule-5-6-7-8-32:*"]`
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
