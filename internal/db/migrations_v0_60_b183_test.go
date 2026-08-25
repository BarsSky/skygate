// v1.5.2 (B183) — drop parent_domain from the device_rules
// natural-key UNIQUE INDEX.
//
// The pre-B183 unique index
//
//   CREATE UNIQUE INDEX device_rules_natural_key_uniq
//     ON device_rules(user_id, device_id, exit_node_id,
//                     target_type, target_value, parent_domain)
//
// let the autoupdater accumulate duplicate rows when
// different parent_domains resolved to the same CIDR
// (e.g. cdn:cloudflare:discordapp.com and
// cdn:cloudflare:discord.com both → 103.21.244.0/22 — two
// separate rows for the same logical rule). Live data for
// emilia: 102 subnet rows, 32 unique subnets. B183 drops
// parent_domain from the natural key so the autoupdater's
// two ON CONFLICT clauses (sync.go) hit a 5-column target
// instead of 6.
//
// These tests pin the migration's contract:
//   1. dedup strategy prefers cdn:-prefixed parent_domain
//      over plain-domain parent_domain (the CDN marker is
//      more informative for the operator)
//   2. ties broken by id DESC (most recent row wins)
//   3. new index has 5 columns (parent_domain removed)
//   4. migration is idempotent (DROP IF EXISTS / CREATE
//      IF NOT EXISTS guards)
//
// Requires a live PG instance (skipped when
// SKYGATE_TEST_PG_DSN is unset, same pattern as
// test_pg_migrations_test.go).

package db

import (
	"database/sql"
	"testing"
)

// b183SetupDeviceRule creates the bare-minimum user +
// device + exit_node skeleton that device_rules FKs
// against, then inserts the given (user, device, exit, type,
// value, parent_domain) row. The skeleton is idempotent —
// if the user already exists (from a prior test), the FK
// errors are swallowed via ON CONFLICT DO NOTHING.
func b183Setup(t *testing.T, d *sql.DB, username string, userID, deviceID int, exitNodeID, targetType, targetValue, parentDomain string) {
	t.Helper()
	// Insert user (ON CONFLICT to ignore if already there
	// from a prior test that shared the username — username
	// is unique in portal_users).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, created_at) VALUES ($1, $2, 'test-hash', false, 1) ON CONFLICT (id) DO NOTHING`,
		userID, username,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// Insert device (node_owner_map has no FK from
	// device_rules, so the node_id can be any int).
	if _, err := d.Exec(
		`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname, os, device_type) VALUES ($1, $2, $3, '', 1, 1, $4, 'linux', 'client') ON CONFLICT (node_id) DO NOTHING`,
		deviceID, userID, username, "device-"+username,
	); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	// Insert the device_rule row. action=accept, device_ip
	// placeholder.
	if _, err := d.Exec(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain)
		 VALUES ($1, $2, $3, $4, $5, 'accept', '', $6)`,
		userID, deviceID, exitNodeID, targetType, targetValue, parentDomain,
	); err != nil {
		t.Fatalf("insert device_rule: %v", err)
	}
}

// b183CountRows returns the number of device_rule rows
// with the given (user, device, exit, type, value) — the
// 5-column natural key.
func b183CountRows(t *testing.T, d *sql.DB, userID, deviceID int, exitNodeID, targetType, targetValue string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type=$4 AND target_value=$5`,
		userID, deviceID, exitNodeID, targetType, targetValue,
	).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// b183GetParentDomain returns the parent_domain of the
// single device_rule row matching the natural key
// (test asserts count=1 first).
func b183GetParentDomain(t *testing.T, d *sql.DB, userID, deviceID int, exitNodeID, targetType, targetValue string) string {
	t.Helper()
	var p string
	if err := d.QueryRow(
		`SELECT parent_domain FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type=$4 AND target_value=$5 LIMIT 1`,
		userID, deviceID, exitNodeID, targetType, targetValue,
	).Scan(&p); err != nil {
		t.Fatalf("get parent_domain: %v", err)
	}
	return p
}

// b183CleanupAll wipes the tables that b183Setup touches
// so tests don't leak state into each other.
func b183CleanupAll(t *testing.T, d *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`TRUNCATE device_rules`,
		`TRUNCATE node_owner_map`,
		`TRUNCATE portal_users CASCADE`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Logf("cleanup %q: %v (non-fatal)", stmt, err)
		}
	}
}

// TestMigrateV060PG_DedupPrefersCDNMarker — three rows for
// the same natural key with DIFFERENT parent_domain
// values. After dedup, exactly one row remains — the one
// with cdn: prefix (the most-recent-id one, since both
// cdn: rows are equal on the ORDER BY rank).
func TestMigrateV060PG_DedupPrefersCDNMarker(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	b183Setup(t, d, "b183alice", 101, 101, "emilia", "subnet", "103.21.244.0/22", "discordapp.com")
	b183Setup(t, d, "b183alice", 101, 101, "emilia", "subnet", "103.21.244.0/22", "cdn:cloudflare:discordapp.com")
	b183Setup(t, d, "b183alice", 101, 101, "emilia", "subnet", "103.21.244.0/22", "cdn:cloudflare:discord.com")

	if got := b183CountRows(t, d, 101, 101, "emilia", "subnet", "103.21.244.0/22"); got != 3 {
		t.Fatalf("setup: expected 3 rows, got %d", got)
	}

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("migrateV060PG: %v", err)
	}

	if got := b183CountRows(t, d, 101, 101, "emilia", "subnet", "103.21.244.0/22"); got != 1 {
		t.Errorf("after dedup: expected 1 row, got %d", got)
	}
	// Two cdn: rows were inserted (b183alice user_id=101,
	// device_id=101, target_value='103.21.244.0/22' with
	// cdn:cloudflare:discord.com inserted LAST → highest
	// id → winner).
	winner := b183GetParentDomain(t, d, 101, 101, "emilia", "subnet", "103.21.244.0/22")
	if winner != "cdn:cloudflare:discord.com" {
		t.Errorf("expected winner parent_domain=cdn:cloudflare:discord.com (most recent id), got %q", winner)
	}
}

// TestMigrateV060PG_DedupNoCDN — when all duplicates have
// plain-domain parent_domains (no cdn: prefix), the
// migration keeps the most recent id (highest id wins).
func TestMigrateV060PG_DedupNoCDN(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	b183Setup(t, d, "b183bob", 102, 102, "karolina", "subnet", "8.8.8.0/24", "dns1.google")
	b183Setup(t, d, "b183bob", 102, 102, "karolina", "subnet", "8.8.8.0/24", "dns2.google")

	if got := b183CountRows(t, d, 102, 102, "karolina", "subnet", "8.8.8.0/24"); got != 2 {
		t.Fatalf("setup: expected 2 rows, got %d", got)
	}

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("migrateV060PG: %v", err)
	}

	if got := b183CountRows(t, d, 102, 102, "karolina", "subnet", "8.8.8.0/24"); got != 1 {
		t.Errorf("after dedup: expected 1 row, got %d", got)
	}
	// No cdn: prefix → tie broken by id DESC → dns2.google
	// (the second-inserted one) wins.
	winner := b183GetParentDomain(t, d, 102, 102, "karolina", "subnet", "8.8.8.0/24")
	if winner != "dns2.google" {
		t.Errorf("expected winner parent_domain=dns2.google (most recent id), got %q", winner)
	}
}

// TestMigrateV060PG_NoDuplicates — when there are no
// duplicate rows, the migration is a no-op for data
// (just DROP+CREATE the index). Verifies the migration
// is safe to run on a clean DB.
func TestMigrateV060PG_NoDuplicates(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	b183Setup(t, d, "b183carol", 103, 103, "sharlotta", "subnet", "1.1.1.0/24", "one.one.one.net")
	b183Setup(t, d, "b183carol", 103, 103, "sharlotta", "subnet", "8.8.4.0/24", "dns.google")
	b183Setup(t, d, "b183carol", 103, 103, "sharlotta", "subnet", "9.9.9.0/24", "dns.quad9.net")

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("migrateV060PG: %v", err)
	}

	// All 3 rows preserved (different natural keys)
	for _, cidr := range []string{"1.1.1.0/24", "8.8.4.0/24", "9.9.9.0/24"} {
		if got := b183CountRows(t, d, 103, 103, "sharlotta", "subnet", cidr); got != 1 {
			t.Errorf("after migration, expected 1 row for %s, got %d", cidr, got)
		}
	}
}

// TestMigrateV060PG_NewIndexHas5Columns — verifies the new
// index has exactly 5 columns. If the migration regressed
// to 6 columns (with parent_domain), the autoupdater's
// 5-column ON CONFLICT would still work but the
// "natural key" semantics would be wrong.
func TestMigrateV060PG_NewIndexHas5Columns(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("migrateV060PG: %v", err)
	}

	// Query PG's pg_index system catalog for the column
	// list of the index. ORDER BY indkey position so we
	// get columns in their natural-key order.
	rows, err := d.Query(`
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE c.relname = 'device_rules_natural_key_uniq'
		ORDER BY array_position(i.indkey, a.attnum)
	`)
	if err != nil {
		t.Fatalf("pg_index query: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols = append(cols, c)
	}
	want := []string{"user_id", "device_id", "exit_node_id", "target_type", "target_value"}
	if len(cols) != len(want) {
		t.Fatalf("index has %d columns %v, want %d: %v", len(cols), cols, len(want), want)
	}
	for i, c := range cols {
		if c != want[i] {
			t.Errorf("index column %d: got %q, want %q", i, c, want[i])
		}
	}
}

// TestMigrateV060PG_Idempotent — running the migration
// twice produces the same final state. The migration uses
// DROP IF EXISTS / CREATE IF NOT EXISTS guards.
func TestMigrateV060PG_Idempotent(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	b183Setup(t, d, "b183dave", 104, 104, "emilia", "subnet", "104.16.0.0/12", "cdn:cloudflare:foo.com")
	b183Setup(t, d, "b183dave", 104, 104, "emilia", "subnet", "104.16.0.0/12", "foo.com")

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("first migrateV060PG: %v", err)
	}
	first := b183CountRows(t, d, 104, 104, "emilia", "subnet", "104.16.0.0/12")

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("second migrateV060PG: %v", err)
	}
	second := b183CountRows(t, d, 104, 104, "emilia", "subnet", "104.16.0.0/12")

	if first != 1 || second != 1 {
		t.Errorf("expected 1 row after each migration, got first=%d second=%d", first, second)
	}
}

// TestMigrateV060PG_PreservesDistinctNaturalKeys — the
// dedup is per (user, device, exit, type, value). Two rows
// with the same parent_domain but DIFFERENT natural keys
// must be preserved.
func TestMigrateV060PG_PreservesDistinctNaturalKeys(t *testing.T) {
	d := openTestDB(t)
	b183CleanupAll(t, d)

	// Same parent_domain, two different user_ids
	b183Setup(t, d, "b183e1", 105, 105, "emilia", "subnet", "10.0.0.0/8", "shared-marker")
	b183Setup(t, d, "b183e2", 106, 106, "emilia", "subnet", "10.0.0.0/8", "shared-marker")
	// Same parent_domain, same user, different device
	b183Setup(t, d, "b183e1", 105, 107, "emilia", "subnet", "10.0.0.0/8", "shared-marker")
	// Same parent_domain, same user+device, different target
	b183Setup(t, d, "b183e1", 105, 105, "emilia", "subnet", "192.168.0.0/16", "shared-marker")

	if err := migrateV060PG(d); err != nil {
		t.Fatalf("migrateV060PG: %v", err)
	}

	// 4 rows preserved — all have distinct natural keys
	if got := b183CountRows(t, d, 105, 105, "emilia", "subnet", "10.0.0.0/8"); got != 1 {
		t.Errorf("user105/device105/10.0.0.0/8: expected 1, got %d", got)
	}
	if got := b183CountRows(t, d, 106, 106, "emilia", "subnet", "10.0.0.0/8"); got != 1 {
		t.Errorf("user106/device106/10.0.0.0/8: expected 1, got %d", got)
	}
	if got := b183CountRows(t, d, 105, 107, "emilia", "subnet", "10.0.0.0/8"); got != 1 {
		t.Errorf("user105/device107/10.0.0.0/8: expected 1, got %d", got)
	}
	if got := b183CountRows(t, d, 105, 105, "emilia", "subnet", "192.168.0.0/16"); got != 1 {
		t.Errorf("user105/device105/192.168.0.0/16: expected 1, got %d", got)
	}
}
