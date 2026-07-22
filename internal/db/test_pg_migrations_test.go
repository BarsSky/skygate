package db

import (
	"database/sql"
	"os"
	"sort"
	"testing"
	"time"
)

// TestMigratePostgresLive is a live test against a real PG
// instance. Skipped unless SKYGATE_TEST_PG_DSN is set. Used
// during the v0.27.0 migration to verify the auto-generated
// migrations_pg.go file runs against an actual PG (not just
// that it compiles).
//
// Set SKYGATE_TEST_PG_DSN=postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable
// to enable.
func TestMigratePostgresLive(t *testing.T) {
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("SKYGATE_TEST_PG_DSN not set; skipping live PG test")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	defer d.Close()

	if got := BackendOf(d); got != BackendPostgres {
		t.Errorf("BackendOf = %q, want %q", got, BackendPostgres)
	}

	// Drop all tables to start fresh. (CASCADE removes FKs.)
	dropAll := `DROP TABLE IF EXISTS
		mesh_members, meshes, invite_codes, headscale_releases,
		user_subnet_shares, user_subnets, telegram_rate_limit,
		telegram_login_tokens, telegram_bindings, telegram_alerts,
		exit_node_state_changes, exit_node_health,
		personal_api_tokens, exit_rule_logs, acl_snapshots,
		device_rules, exit_servers, node_owner_map, audit_log,
		devices, preauth_keys, global_settings, portal_users
		CASCADE`
	if _, err := d.Exec(dropAll); err != nil {
		t.Fatalf("dropAll: %v", err)
	}

	// Run all migrations in the same order as db.go's
	// migratePostgres (V025 first because V020+ FK to it).
	for _, fn := range []func(*sql.DB) error{
		migrateV025PG, migrateV020PG, migrateV021PG, migrateV022PG,
		migrateV023PG, migrateV024PG, migrateV026PG, migrateV027PG,
		migrateV028PG, migrateV029PG, migrateV030PG, migrateV031PG,
		migrateV032PG, migrateV033PG, migrateV034PG, migrateV035PG,
		migrateV036PG, migrateV037PG, migrateV038PG, migrateV039PG,
		migrateV041PG, migrateV042PG, migrateV043PG,
	} {
		if err := fn(d); err != nil {
			t.Errorf("migration failed: %v", err)
		}
	}

	// Verify tables exist.
	want := []string{
		"acl_snapshots", "audit_log", "device_rules", "devices",
		"exit_node_health", "exit_node_state_changes",
		"exit_rule_logs", "exit_servers", "global_settings",
		"headscale_releases", "invite_codes", "mesh_members",
		"meshes", "node_owner_map", "personal_api_tokens",
		"portal_users", "preauth_keys", "telegram_alerts",
		"telegram_bindings", "telegram_login_tokens",
		"telegram_rate_limit", "user_subnet_shares",
		"user_subnets",
	}
	rows, err := d.Query(`SELECT tablename FROM pg_tables WHERE schemaname='public'`)
	if err != nil {
		t.Fatalf("Query pg_tables: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Errorf("Scan: %v", err)
			continue
		}
		got[name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing table: %s", w)
		}
	}
	gotNames := make([]string, 0, len(got))
	for n := range got {
		gotNames = append(gotNames, n)
	}
	sort.Strings(gotNames)
	t.Logf("PG tables created: %v", gotNames)

	// Smoke-test the data path: create a user + an audit row + a
	// device rule. Verifies the placeholders + INSERT OR REPLACE
	// conversions actually work against a live PG.
	username := "pgtest"
	pwhash := "x"
	now := time.Now().Unix()
	var userID int64
	if err := d.QueryRow(
		`INSERT INTO portal_users (username, password_hash, is_admin, theme, created_at)
		 VALUES ($1, $2, 0, 'linear', $3) RETURNING id`,
		username, pwhash, now,
	).Scan(&userID); err != nil {
		t.Fatalf("INSERT portal_users: %v", err)
	}
	if userID <= 0 {
		t.Errorf("userID = %d, want > 0", userID)
	}

	// audit_log
	if err := AppendAuditLog(d, userID, username, "pg_test", "live migration test"); err != nil {
		t.Errorf("AppendAuditLog: %v", err)
	}

	// device_rules (the FK to portal_users must work)
	var ruleID int64
	if err := d.QueryRow(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action)
		 VALUES ($1, 1, 'node-1', 'domain', 'example.com', 'accept') RETURNING id`,
		userID,
	).Scan(&ruleID); err != nil {
		t.Errorf("INSERT device_rules: %v", err)
	}
	if ruleID <= 0 {
		t.Errorf("ruleID = %d, want > 0", ruleID)
	}

	// INSERT OR REPLACE path: qInsertOrReplaceNodeOwner (the
	// port-migrations generator should have replaced this with
	// ON CONFLICT ... DO UPDATE SET).
	var nodeID string = "test-node-1"
	var replaceCount int
	if err := d.QueryRow(
		`INSERT INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (node_id) DO UPDATE SET
				headscale_user_id = EXCLUDED.headscale_user_id,
				username = EXCLUDED.username,
				tag = EXCLUDED.tag,
				tagged_by_user_id = EXCLUDED.tagged_by_user_id,
				tagged_at = EXCLUDED.tagged_at
			RETURNING 1`,
		nodeID, 100, username, "tag:private", 1, now,
	).Scan(&replaceCount); err != nil {
		t.Errorf("INSERT ... ON CONFLICT DO UPDATE: %v", err)
	}

	// INSERT OR IGNORE path: InsertIgnoreNodeOwner (should
	// be ON CONFLICT DO NOTHING now).
	if err := InsertIgnoreNodeOwner(d, nodeID, 100, username, "tag:private", 1); err != nil {
		t.Errorf("InsertIgnoreNodeOwner: %v", err)
	}

	// SELECT the data back.
	gotUsername, err := GetUserNameByID(d, userID)
	if err != nil {
		t.Errorf("GetUserNameByID: %v", err)
	}
	if gotUsername != username {
		t.Errorf("GetUserNameByID = %q, want %q", gotUsername, username)
	}

	// Count audit rows for the test user.
	rows2, err := d.Query(
		`SELECT count(*) FROM audit_log WHERE user_id = $1 AND action = $2`,
		userID, "pg_test",
	)
	if err != nil {
		t.Errorf("SELECT audit_log: %v", err)
	}
	defer rows2.Close()
	var n int
	for rows2.Next() {
		if err := rows2.Scan(&n); err != nil {
			t.Errorf("Scan audit count: %v", err)
		}
	}
	if n != 1 {
		t.Errorf("audit count = %d, want 1", n)
	}

	t.Logf("PG live data path OK: userID=%d, ruleID=%d, replaceCount=%d", userID, ruleID, replaceCount)
}
