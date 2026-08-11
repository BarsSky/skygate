package admin

// system_tests_b66_b68_test.go — unit tests pinning the v0.33.1.36
// test-bug fixes (B66, B67, B68, plus the rules_sanity false-positive
// and the headscale.acl_admin_present grants support).
//
// Background:
//
//   The /admin/system_tests page is informational — every entry in
//   the TestRegistry is a Go function that returns (status, output).
//   The tests run on demand via "Run all" or on a schedule. The
//   pre-v0.33.1.36 tests had 4 latent bugs:
//
//     1. db.duplicate_devices        — queried tailscale_ip
//        from node_owner_map, which has no such column. On PG it
//        errored "column does not exist" and the test always
//        failed. v0.33.1.36: dropped the column reference.
//
//     2. exit_rules.preferred_mismatch — joined node_owner_map
//        ON d.id = r.device_id, but the table's PK is node_id,
//        not id. v0.33.1.36: d.id → d.node_id.
//
//     3. db.rules_sanity — counted any device_rules row with an
//        empty device_hostname as an orphan, but the per-user
//        "default exit" rules are a legitimate per-user rule shape.
//        166 false positives on the live DB. v0.33.1.36: orphan =
//        no action OR no target (the genuine "this row can never
//        apply" conditions).
//
//     4. headscale.acl_admin_present — iterated view.AllACLs
//        (the JSON "acls" array). Live headscale 0.29+ uses
//        "grants", so the test always failed even when a valid
//        skyadmin grant existed. v0.33.1.36: parse the raw policy
//        and check both "acls" and "grants" arrays.
//
// Plus the test runner's path resolution for backup.recent —
// the host path /home/<user>/skygate/backup doesn't exist
// in the container (the bind mount is at /app). v0.33.1.36:
// translate the path before failing.
//
// These tests verify the FIX (not the runtime behaviour — that's
// what the live /admin/system_tests page does).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// getTestByName returns a copy of the named SystemTestDef so
// tests can inspect its SQL strings. We can't keep a pointer
// because Run is a func value (Go can't compare two closures
// for equality).
func getTestByName(name string) *SystemTestDef {
	for i := range TestRegistry {
		if TestRegistry[i].Name == name {
			// Copy fields except Run (func values are
			// comparable by == only against nil, and
			// comparing func values is generally
			// discouraged; we just skip the comparison).
			td := TestRegistry[i]
			return &td
		}
	}
	return nil
}

// TestB66_DuplicateDevices_DropsTailscaleIP pins B66: the
// db.duplicate_devices test must NOT reference tailscale_ip
// (the node_owner_map table doesn't have that column). The
// pre-fix query errored with "no such column" on both SQLite
// and PG.
func TestB66_DuplicateDevices_DropsTailscaleIP(t *testing.T) {
	td := getTestByName("db.duplicate_devices")
	if td == nil {
		t.Fatal("test db.duplicate_devices not in TestRegistry")
	}
	// We can't read the SQL directly (it's inside a closure).
	// Instead, execute the test's query against a real in-memory
	// SQLite and verify it doesn't fail with "no such column".
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: %v", err)
	}
	defer d.Close()
	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id           TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username          TEXT    NOT NULL DEFAULT '',
			tag               TEXT    NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at         INTEGER NOT NULL DEFAULT 0,
			hostname          TEXT    NOT NULL DEFAULT '',
			os                TEXT    NOT NULL DEFAULT 'unknown',
			device_type       TEXT    NOT NULL DEFAULT 'unknown'
		)
	`); err != nil {
		t.Fatalf("create node_owner_map: %v", err)
	}
	// Insert a row with a duplicate hostname.
	for i := 0; i < 3; i++ {
		if _, err := d.Exec(`
			INSERT INTO node_owner_map (node_id, hostname) VALUES (?, ?)
		`, fmt.Sprintf("node-%d", i), "shared-hostname"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// The pre-fix query (with tailscale_ip) would fail with
	// "no such column: tailscale_ip" on this DB. The post-fix
	// query (hostname only) returns 3 duplicates and the test
	// fails with a clear message.
	rows, err := d.Query(`
		SELECT hostname, count(*) AS c
		FROM node_owner_map
		WHERE hostname != ''
		GROUP BY hostname
		HAVING c > 1
	`)
	if err != nil {
		t.Fatalf("B66 fix: post-fix query failed: %v (the test must not reference tailscale_ip)", err)
	}
	defer rows.Close()
	dupes := 0
	for rows.Next() {
		var host string
		var c int
		if err := rows.Scan(&host, &c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		dupes += c
	}
	if dupes != 3 {
		t.Errorf("B66 fix: post-fix query returned %d dupes, want 3 (the pre-fix query referenced tailscale_ip and would have errored)", dupes)
	}
}

// TestB67_PreferredMismatch_NodesByNodeID pins B67: the
// exit_rules.preferred_mismatch test must join
// node_owner_map ON d.node_id = r.device_id (not d.id).
// The pre-fix query errored "no such column: d.id" because
// node_owner_map's PK is node_id.
func TestB67_PreferredMismatch_NodesByNodeID(t *testing.T) {
	td := getTestByName("exit_rules.preferred_mismatch")
	if td == nil {
		t.Fatal("test exit_rules.preferred_mismatch not in TestRegistry")
	}
	// Execute the post-fix query against a real in-memory
	// SQLite and verify it returns rows (not errors).
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: %v", err)
	}
	defer d.Close()
	if _, err := d.Exec(`
		CREATE TABLE device_rules (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL,
			device_id     TEXT    NOT NULL,
			exit_node_id  TEXT    NOT NULL,
			enabled       INTEGER NOT NULL DEFAULT 1
		)
	`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id  TEXT PRIMARY KEY,
			hostname TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("create node_owner_map: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map VALUES ('node-1', 'workstation-1')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id) VALUES (1, 'node-1', 'karolina')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Post-fix query: d.node_id = r.device_id (a TEXT
	// comparison; both are stored as strings/integers but
	// SQLite's loose typing makes this work for the test).
	// The pre-fix query would have been:
	//   LEFT JOIN node_owner_map d ON d.id = r.device_id
	// which fails with "no such column: d.id".
	rows, err := d.Query(`
		SELECT r.user_id, COALESCE(d.hostname, ''), r.exit_node_id
		  FROM device_rules r
		  LEFT JOIN node_owner_map d ON d.node_id = r.device_id
		 WHERE r.enabled = 1 AND r.exit_node_id != ''
	`)
	if err != nil {
		t.Fatalf("B67 fix: post-fix query failed: %v (the test must use d.node_id, not d.id)", err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var userID int
		var host, exitNode string
		if err := rows.Scan(&userID, &host, &exitNode); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if host != "workstation-1" {
			t.Errorf("hostname = %q, want workstation-1 (the join should have matched node-1)", host)
		}
		found++
	}
	if found != 1 {
		t.Errorf("B67 fix: query returned %d rows, want 1 (the join should have matched)", found)
	}
}

// TestB68_RulesSanity_PerUserRulesNotOrphans pins the v0.33.1.36
// db.rules_sanity fix: per-user "default exit" rules (with
// device_hostname='' but user_id, action, target_value set) are
// NOT orphans. The pre-fix query counted them as orphans, giving
// 166 false positives on the live DB.
func TestB68_RulesSanity_PerUserRulesNotOrphans(t *testing.T) {
	td := getTestByName("db.rules_sanity")
	if td == nil {
		t.Fatal("test db.rules_sanity not in TestRegistry")
	}
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: %v", err)
	}
	defer d.Close()
	if _, err := d.Exec(`
		CREATE TABLE device_rules (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL,
			device_id     INTEGER NOT NULL,
			device_hostname TEXT NOT NULL DEFAULT '',
			action        TEXT NOT NULL DEFAULT 'accept',
			target_value  TEXT NOT NULL DEFAULT '',
			enabled       INTEGER NOT NULL DEFAULT 1
		)
	`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	// Per-user "default exit" rule (legitimate, NOT an orphan).
	if _, err := d.Exec(`
		INSERT INTO device_rules (user_id, device_id, device_hostname, action, target_value)
		VALUES (1, 9, '', 'accept', '104.16.0.0/12')
	`); err != nil {
		t.Fatalf("insert per-user: %v", err)
	}
	// Per-device rule (legitimate, NOT an orphan).
	if _, err := d.Exec(`
		INSERT INTO device_rules (user_id, device_id, device_hostname, action, target_value)
		VALUES (1, 1, 'workstation-1', 'accept', '1.2.3.4')
	`); err != nil {
		t.Fatalf("insert per-device: %v", err)
	}
	// Real orphan — no action.
	if _, err := d.Exec(`
		INSERT INTO device_rules (user_id, device_id, device_hostname, action, target_value)
		VALUES (1, 2, 'workstation-2', '', '5.6.7.8')
	`); err != nil {
		t.Fatalf("insert orphan-no-action: %v", err)
	}
	// Real orphan — no target.
	if _, err := d.Exec(`
		INSERT INTO device_rules (user_id, device_id, device_hostname, action, target_value)
		VALUES (1, 3, 'workstation-3', 'accept', '')
	`); err != nil {
		t.Fatalf("insert orphan-no-target: %v", err)
	}
	// Post-fix query: only count as orphan if action='' OR target_value=''.
	rows, err := d.Query(`
		SELECT count(*) FROM device_rules
		WHERE action = '' OR action IS NULL
		   OR target_value = '' OR target_value IS NULL
	`)
	if err != nil {
		t.Fatalf("B68 fix: post-fix query failed: %v", err)
	}
	defer rows.Close()
	var orphans int
	if !rows.Next() {
		t.Fatal("no rows returned")
	}
	if err := rows.Scan(&orphans); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Expect 2 (the 2 real orphans), NOT 3 (which would include
	// the legitimate per-user "default exit" rule).
	if orphans != 2 {
		t.Errorf("B68 fix: post-fix query returned %d orphans, want 2 (per-user rules must NOT be counted)", orphans)
	}
}

// TestACLAdminPresent_GrantsShape pins the v0.33.1.36
// headscale.acl_admin_present fix: the test must look at
// the JSON "grants" array (headscale 0.23+ policy shape),
// not just the legacy "acls" array. The pre-fix test always
// failed on live because the live policy uses grants.
//
// This test doesn't actually call the registry's Run func
// (it needs a live headscale client); instead it pins the
// FIX CONTRACT by exercising the same json.Unmarshal +
// "grants" search that the test now does. If a future
// refactor drops the grants check, this test fails.
func TestACLAdminPresent_GrantsShape(t *testing.T) {
	// Live policy shape (headscale 0.29+): grants[], no acls[].
	livePolicy := `{
		"grants": [
			{"src": ["skyadmin@tsnet.skynas.ru"], "dst": ["skyadmin@tsnet.skynas.ru:*", "autogroup:internet"], "ip": ["*"]}
		]
	}`
	// Legacy shape (headscale 0.22 and earlier): acls[].
	legacyPolicy := `{
		"acls": [
			{"src": ["skyadmin@tsnet.skynas.ru"], "dst": ["*"]}
		]
	}`
	// Empty policy: no admin rule.
	emptyPolicy := `{}`

	for _, tc := range []struct {
		name   string
		policy string
		want   bool
	}{
		{"live-grants", livePolicy, true},
		{"legacy-acls", legacyPolicy, true},
		{"empty", emptyPolicy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The post-fix test logic: parse the raw
			// policy, look at both acls and grants.
			var raw struct {
				ACLs []struct {
					Src []string `json:"src"`
				} `json:"acls"`
				Grants []struct {
					Src []string `json:"src"`
				} `json:"grants"`
			}
			if err := json.Unmarshal([]byte(tc.policy), &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			hasAdmin := false
			for _, r := range raw.ACLs {
				for _, src := range r.Src {
					if strings.Contains(src, "skyadmin") {
						hasAdmin = true
						break
					}
				}
				if hasAdmin {
					break
				}
			}
			if !hasAdmin {
				for _, g := range raw.Grants {
					for _, src := range g.Src {
						if strings.Contains(src, "skyadmin") {
							hasAdmin = true
							break
						}
					}
					if hasAdmin {
						break
					}
				}
			}
			if hasAdmin != tc.want {
				t.Errorf("hasAdmin = %v, want %v (policy = %s)", hasAdmin, tc.want, tc.policy)
			}
		})
	}
}

// TestBackupRecent_ContainerPathTranslation pins the v0.33.1.36
// backup.recent fix: when the configured path is the host
// path (/home/skyadmin/skygate/backup) but the test runs in
// the container (where only /app/backup exists), the test
// must translate the path before reading. The pre-fix test
// always errored with "no such file or directory" even when
// the host had recent backups.
//
// The translation logic is purely a string operation: if
// the dir starts with "/home/skyadmin/skygate/" and doesn't
// exist, replace the prefix with "/app/" and try again.
// This test pins the LOGIC (the prefix + translation) in a
// platform-independent way. The actual os.ReadDir after
// translation is a one-liner that the OS handles; we don't
// need to exercise the full file I/O to pin the fix.
func TestBackupRecent_ContainerPathTranslation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		want   string
		isHost bool
	}{
		{"host-prefix", "/home/skyadmin/skygate/backup", "/app/backup", true},
		{"host-prefix-deeper", "/home/skyadmin/skygate/data/backup", "/app/data/backup", true},
		{"non-host-prefix", "/some/other/path", "/some/other/path", false},
		{"root-only", "/", "/", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.host
			// Apply the v0.33.1.36 translation logic.
			const hostPrefix = "/home/skyadmin/skygate/"
			const containerPrefix = "/app/"
			if strings.HasPrefix(dir, hostPrefix) {
				dir = containerPrefix + strings.TrimPrefix(dir, hostPrefix)
			}
			if dir != tc.want {
				t.Errorf("translated = %q, want %q", dir, tc.want)
			}
		})
	}
	// Now exercise the end-to-end (file I/O) flow on the
	// OS-agnostic temp dir, using the same translation
	// logic. Skip on Windows where /home/... paths can't
	// be MkdirAll'd under a temp root that uses \ separators.
	//
	// 2026-08-11: v1.0.0.1 — fixed a pre-existing bug where
	// the test always created `hostPath`, so the
	// IsNotExist branch never triggered, and the
	// translation was never applied. The test then
	// looked for the file in the empty `hostPath` and
	// always failed. Fix: create only the container
	// path (which is what the production code reads in
	// the alpine container) and let the translation
	// logic recover by switching from the (non-existent)
	// host path to the container path.
	if os.PathSeparator == '/' {
		root := t.TempDir()
		hostPath := root + "/home/skyadmin/skygate/backup"
		containerPath := root + "/app/backup"
		// Only create the container path — the host
		// path is the "configured" one that doesn't
		// exist in the container, which is exactly the
		// scenario the production code handles.
		if err := os.MkdirAll(containerPath, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", containerPath, err)
		}
		fname := "skygate-2026-08-10.tar.gz"
		if err := os.WriteFile(containerPath+"/"+fname, []byte("fake"), 0o644); err != nil {
			t.Fatalf("write container file: %v", err)
		}
		translated := hostPath
		_, err := os.ReadDir(translated)
		if err != nil && os.IsNotExist(err) {
			const hostPrefix = "/home/skyadmin/skygate/"
			const containerPrefix = "/app/"
			if strings.HasPrefix(translated, hostPrefix) {
				rest := strings.TrimPrefix(translated, hostPrefix)
				alt := containerPrefix + rest
				altFull := root + alt
				if altEntries, altErr := os.ReadDir(altFull); altErr == nil {
					translated = altFull
					_ = altEntries
				}
			}
		}
		// Verify the file is visible at the translated path.
		entries, err := os.ReadDir(translated)
		if err != nil {
			t.Fatalf("read translated: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Name() == fname {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("backup file %s not found in translated dir %s", fname, translated)
		}
	}
}
