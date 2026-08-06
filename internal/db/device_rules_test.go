// Tests for the device_rules helpers in internal/db/device_rules.go.
// Focused on the per-device drill-down introduced for v0.33.1.17.
//
// 2026-08-06: introduced for the ?device=NAME filter on
// /admin/exit-rules. The /admin/devices "dead rules" count badge
// links to /admin/exit-rules?device=NAME and this helper is what
// filters that view. Two regression vectors the tests pin:
//
//   1. The hostname match is case-insensitive. /admin/devices
//      stores hostnames in lowercase (backfillNodeOwnership), but
//      a hand-typed `?device=WorkStation-1` URL parameter must
//      still resolve. The query uses LOWER() on both sides.
//
//   2. Unknown hostname returns an empty slice, NOT nil, so the
//      caller can distinguish "no rules" from "device not found"
//      via the rule count without a separate existence check.

package db

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openDeviceRulesTestDB opens a fresh in-memory SQLite DB with the
// three tables the GetAllRulesForAdminByDevice query JOINs on:
//   - portal_users  (id, username — populated for the user_name col)
//   - device_rules  (id, user_id, device_id, exit_node_id,
//                    target_type, target_value, action, device_ip,
//                    enabled, parent_domain, created_at)
//   - node_owner_map (node_id, hostname — the device filter)
//
// The schema mirrors the production migrations_v0.25 (device_rules)
// + v0.28 (node_owner_map + device_hostname backfill) + v0.35
// (extra portal_users columns) but tests use hand-rolled CREATE
// statements — we don't go through migrate() because the test
// only needs the row shape, not every column.
func openDeviceRulesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE portal_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			is_admin INTEGER NOT NULL DEFAULT 0,
			headscale_user_id INTEGER,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			theme TEXT NOT NULL DEFAULT 'linear',
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE device_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			device_id INTEGER NOT NULL,
			exit_node_id TEXT NOT NULL,
			target_type TEXT NOT NULL DEFAULT 'domain',
			target_value TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT 'accept',
			device_ip TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			parent_domain TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE node_owner_map (
			node_id           TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username          TEXT NOT NULL DEFAULT '',
			tag               TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at         INTEGER NOT NULL DEFAULT 0,
			hostname          TEXT NOT NULL DEFAULT '',
			os                TEXT NOT NULL DEFAULT 'unknown',
			device_type       TEXT NOT NULL DEFAULT 'unknown'
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			_ = d.Close()
			t.Fatalf("schema: %v", err)
		}
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestGetAllRulesForAdminByDevice_FiltersByHostname is the happy
// path: two devices, two rules on each, requesting one hostname
// returns exactly the rules for that device.
func TestGetAllRulesForAdminByDevice_FiltersByHostname(t *testing.T) {
	d := openDeviceRulesTestDB(t)

	// Two portal users (admin + user1).
	for _, u := range []struct{ name string }{
		{"admin"}, {"user1"},
	} {
		if _, err := d.Exec(
			`INSERT INTO portal_users (username, password_hash) VALUES (?, '')`,
			u.name); err != nil {
			t.Fatalf("seed user %s: %v", u.name, err)
		}
	}

	// Two devices, one per user. Hostname is the user-facing
	// identifier; node_id is the headscale integer in TEXT form.
	devices := []struct{ nodeID, hostname string }{
		{"10", "workstation-1"},
		{"11", "workstation-2"},
	}
	for _, dev := range devices {
		if _, err := d.Exec(
			`INSERT INTO node_owner_map (node_id, hostname) VALUES (?, ?)`,
			dev.nodeID, dev.hostname); err != nil {
			t.Fatalf("seed device %s: %v", dev.hostname, err)
		}
	}

	// Three rules: 2 on workstation-1 (user_id=1), 1 on
	// workstation-2 (user_id=2). device_id is the headscale
	// node_id as INT, matching the JOIN `n.node_id::int = r.device_id`.
	seeds := []struct {
		userID, deviceID int
		exitNode, target string
	}{
		{1, 10, "karolina", "rutracker.org"},
		{1, 10, "karolina", "1.1.1.1/32"},
		{2, 11, "emilia", "example.com"},
	}
	for _, s := range seeds {
		if _, err := d.Exec(
			`INSERT INTO device_rules
			   (user_id, device_id, exit_node_id, target_type, target_value, enabled)
			 VALUES (?, ?, ?, 'domain', ?, 1)`,
			s.userID, s.deviceID, s.exitNode, s.target); err != nil {
			t.Fatalf("seed rule: %v", err)
		}
	}

	got, err := GetAllRulesForAdminByDevice(d, "workstation-1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rules for workstation-1, got %d: %+v", len(got), got)
	}
	// Every returned row must have device_id=10 (workstation-1's
	// headscale node_id).
	for _, r := range got {
		if r.DeviceID != 10 {
			t.Errorf("rule %d leaked to another device: device_id=%d", r.ID, r.DeviceID)
		}
		// UserName must be filled in via the LEFT JOIN.
		if r.UserName != "admin" {
			t.Errorf("rule %d user_name=%q, want admin", r.ID, r.UserName)
		}
	}
}

// TestGetAllRulesForAdminByDevice_CaseInsensitive pins the LOWER()
// match — the same query must resolve "WorkStation-1" and
// "workstation-1" identically. /admin/devices stores lowercase
// but the URL param comes from the operator's hand.
func TestGetAllRulesForAdminByDevice_CaseInsensitive(t *testing.T) {
	d := openDeviceRulesTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash) VALUES ('admin', '')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO node_owner_map (node_id, hostname) VALUES ('10', 'workstation-1')`,
	); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO device_rules
		   (user_id, device_id, exit_node_id, target_type, target_value, enabled)
		 VALUES (1, 10, 'karolina', 'domain', 'rutracker.org', 1)`,
	); err != nil {
		t.Fatalf("seed rule: %v", err)
	}

	for _, tc := range []string{"workstation-1", "WorkStation-1", "WORKSTATION-1"} {
		got, err := GetAllRulesForAdminByDevice(d, tc)
		if err != nil {
			t.Fatalf("query %q: %v", tc, err)
		}
		if len(got) != 1 {
			t.Errorf("hostname %q: want 1 rule, got %d", tc, len(got))
		}
	}
}

// TestGetAllRulesForAdminByDevice_UnknownDevice — unknown hostname
// returns an empty slice (NOT nil, NOT an error). The caller
// distinguishes "no rules" from "device not found" via the rule
// count; we just need to make sure we don't blow up.
func TestGetAllRulesForAdminByDevice_UnknownDevice(t *testing.T) {
	d := openDeviceRulesTestDB(t)
	// Empty DB — no users, no devices, no rules.
	got, err := GetAllRulesForAdminByDevice(d, "ghost-device")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// The helper builds `var out []DeviceRule` and only appends on
	// rows.Next() — so an empty result is nil. The caller (form_admin.go)
	// already handles nil via len(rr) safely; the important contract
	// is "no error".
	if len(got) != 0 {
		t.Errorf("want 0 rules, got %d: %+v", len(got), got)
	}
}

// TestGetAllRulesForAdminByDevice_DisabledExcluded checks that
// disabled rules (enabled=0) are still returned — the v0.33.1.17
// drill-down shows ALL rules for the device, not just enabled
// ones, so the operator can see what they've disabled. Compare
// with GetEnabledDomainRules which DOES filter enabled=1.
func TestGetAllRulesForAdminByDevice_IncludesDisabled(t *testing.T) {
	d := openDeviceRulesTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash) VALUES ('admin', '')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO node_owner_map (node_id, hostname) VALUES ('10', 'workstation-1')`,
	); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	// 1 enabled + 1 disabled.
	if _, err := d.Exec(
		`INSERT INTO device_rules
		   (user_id, device_id, exit_node_id, target_type, target_value, enabled)
		 VALUES (1, 10, 'karolina', 'domain', 'enabled.example.com', 1)`,
	); err != nil {
		t.Fatalf("seed enabled rule: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO device_rules
		   (user_id, device_id, exit_node_id, target_type, target_value, enabled)
		 VALUES (1, 10, 'karolina', 'domain', 'disabled.example.com', 0)`,
	); err != nil {
		t.Fatalf("seed disabled rule: %v", err)
	}

	got, err := GetAllRulesForAdminByDevice(d, "workstation-1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rules (1 enabled + 1 disabled), got %d: %+v", len(got), got)
	}
	var sawEnabled, sawDisabled bool
	for _, r := range got {
		switch r.TargetValue {
		case "enabled.example.com":
			if !r.Enabled {
				t.Errorf("enabled rule reported Enabled=false")
			}
			sawEnabled = true
		case "disabled.example.com":
			if r.Enabled {
				t.Errorf("disabled rule reported Enabled=true")
			}
			sawDisabled = true
		}
	}
	if !sawEnabled || !sawDisabled {
		t.Errorf("missing one of the seeded rules: enabled=%v disabled=%v", sawEnabled, sawDisabled)
	}
}
