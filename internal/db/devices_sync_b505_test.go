package db

// devices_sync_b505_test.go — B505 (2026-11-20)
//
// Measured on the reference deployment before this block: `SELECT count(*) FROM
// devices` answered 0 while headscale carried 15 live nodes and node_owner_map a
// row for each — the table was created by the schema, had a query waiting on it,
// and nothing ever wrote it.
//
// These tests pin the projection's four behaviours: insert, update-in-place (a
// rename must NOT create a second row), removal of nodes that left, and the honest
// treatment of a live node that no portal user owns (no row, counted as
// unattributed — never an invented owner).

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
)

var b505DBSeq atomic.Int64

// b505Open opens its own in-memory SQLite database. `cache=shared` with one DSN
// shares rows between tests (measured in B502: the second test hit
// `UNIQUE constraint failed: portal_users.username`), hence the per-test name.
func b505Open(t *testing.T) *sql.DB {
	t.Helper()
	_, conn, err := OpenWithDialect(fmt.Sprintf("file:b505_%d?mode=memory&cache=shared", b505DBSeq.Add(1)))
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := ApplyMigrations(conn, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return conn
}

func b505Seed(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	// The `infra` portal user comes from migration V054 (id 99); `skyadmin` is ours.
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	          VALUES (7, 'skyadmin', 'x', 1, 1)`)
	mustExec(`UPDATE portal_users SET headscale_user_id = 85 WHERE username = 'infra'`)
	// node_owner_map: two skyadmin devices + one infra relay + one unattributed node.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname, os, device_type)
	          VALUES ('24',   1,  'skyadmin', 'tag:dev-skyadmin-a71',      'a71',     'android', 'client'),
	                 ('36',   1,  'skyadmin', 'tag:dev-skyadmin-cyborg',   'cyborg',  'windows', 'client'),
	                 ('3',    85, 'infra',    'tag:dev-infra-emilia',      'emilia',  'linux',   'exit-node')`)
}

func b505DeviceRows(t *testing.T, d *sql.DB) map[string]string {
	t.Helper()
	rows, err := d.Query(`SELECT COALESCE(node_id,''), user_id, hostname, COALESCE(os,''),
	                             COALESCE(last_seen,0), COALESCE(online,0), COALESCE(ip_addresses,'')
	                        FROM devices`)
	if err != nil {
		t.Fatalf("select devices: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var node, host, osName, ips string
		var uid, seen, online int64
		if err := rows.Scan(&node, &uid, &host, &osName, &seen, &online, &ips); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[node] = fmt.Sprintf("uid=%d host=%s os=%s seen=%d online=%d ips=%s", uid, host, osName, seen, online, ips)
	}
	return out
}

// TestB505_ProjectsLiveNodes — the defect: the table was empty.
func TestB505_ProjectsLiveNodes(t *testing.T) {
	d := b505Open(t)
	b505Seed(t, d)
	if n := len(b505DeviceRows(t, d)); n != 0 {
		t.Fatalf("precondition: devices already has %d row(s)", n)
	}

	res, err := SyncDeviceProjection(d, []DeviceState{
		{NodeID: "24", Hostname: "a71", IPs: "100.64.0.16,fd7a:115c:a1e0::14", LastSeen: 1795200000, Online: true},
		{NodeID: "36", Hostname: "cyborg", IPs: "100.64.0.11", LastSeen: 1795199000, Online: false},
		{NodeID: "3", Hostname: "emilia", IPs: "100.64.0.3", LastSeen: 1795199500, Online: true},
	})
	if err != nil {
		t.Fatalf("SyncDeviceProjection: %v", err)
	}
	if res.Written != 3 {
		t.Errorf("Written = %d, want 3", res.Written)
	}
	got := b505DeviceRows(t, d)
	if len(got) != 3 {
		t.Fatalf("devices rows = %d, want 3 (%v)", len(got), got)
	}
	if want := "uid=7 host=a71 os=android seen=1795200000 online=1 ips=100.64.0.16,fd7a:115c:a1e0::14"; got["24"] != want {
		t.Errorf("a71 row = %q, want %q (liveness from headscale, owner+OS from node_owner_map)", got["24"], want)
	}
	if want := "uid=7 host=cyborg os=windows seen=1795199000 online=0 ips=100.64.0.11"; got["36"] != want {
		t.Errorf("cyborg row = %q, want %q", got["36"], want)
	}
	// The infra relay is owned by the infra portal user (id 99 from migration V054).
	if got["3"] != "uid=99 host=emilia os=linux seen=1795199500 online=1 ips=100.64.0.3" {
		t.Errorf("emilia row = %q, want the infra portal user id", got["3"])
	}
}

// TestB505_RenameUpdatesInPlace: a renamed device must not become a second row, and
// the node id is what identifies it.
func TestB505_RenameUpdatesInPlace(t *testing.T) {
	d := b505Open(t)
	b505Seed(t, d)
	if _, err := SyncDeviceProjection(d, []DeviceState{
		{NodeID: "36", Hostname: "desktop-cj8t9me", Online: true, LastSeen: 1},
	}); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	if _, err := SyncDeviceProjection(d, []DeviceState{
		{NodeID: "36", Hostname: "cyborg", Online: true, LastSeen: 2},
	}); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	got := b505DeviceRows(t, d)
	if len(got) != 1 {
		t.Fatalf("devices rows = %d after a rename, want 1 (%v)", len(got), got)
	}
	if got["36"] != "uid=7 host=cyborg os=windows seen=2 online=1 ips=" {
		t.Errorf("row after rename = %q, want the SAME row renamed in place", got["36"])
	}
}

// TestB505_RemovesVanishedNodes — and reports how many.
func TestB505_RemovesVanishedNodes(t *testing.T) {
	d := b505Open(t)
	b505Seed(t, d)
	both := []DeviceState{
		{NodeID: "24", Hostname: "a71", Online: true},
		{NodeID: "36", Hostname: "cyborg", Online: true},
	}
	if _, err := SyncDeviceProjection(d, both); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	res, err := SyncDeviceProjection(d, both[:1])
	if err != nil {
		t.Fatalf("second projection: %v", err)
	}
	if res.Removed != 1 {
		t.Errorf("Removed = %d, want 1 (a71 stays, cyborg left)", res.Removed)
	}
	if got := b505DeviceRows(t, d); len(got) != 1 || got["24"] == "" {
		t.Errorf("after the node left, rows = %v, want only a71", got)
	}
	// Idempotent: the same input again changes nothing.
	res2, err := SyncDeviceProjection(d, both[:1])
	if err != nil {
		t.Fatalf("third projection: %v", err)
	}
	if res2.Removed != 0 || len(b505DeviceRows(t, d)) != 1 {
		t.Errorf("re-running the projection was not idempotent: removed=%d rows=%v", res2.Removed, b505DeviceRows(t, d))
	}
}

// TestB505_UnattributedNodeGetsNoRow: a live node no portal user owns must be
// counted, never attributed by guess (B284/B316's rule).
func TestB505_UnattributedNodeGetsNoRow(t *testing.T) {
	d := b505Open(t)
	b505Seed(t, d)
	res, err := SyncDeviceProjection(d, []DeviceState{
		{NodeID: "999", Hostname: "stranger", Online: true},
		{NodeID: "24", Hostname: "a71", Online: true},
	})
	if err != nil {
		t.Fatalf("SyncDeviceProjection: %v", err)
	}
	if res.Unattributed != 1 {
		t.Errorf("Unattributed = %d, want 1", res.Unattributed)
	}
	got := b505DeviceRows(t, d)
	if _, ok := got["999"]; ok {
		t.Errorf("an unowned node got a row: %v", got)
	}
	if len(got) != 1 {
		t.Errorf("rows = %d, want 1 (only the owned node)", len(got))
	}
}

// TestB505_NilDBIsANoOp keeps the tick safe when the pool is unavailable.
func TestB505_NilDBIsANoOp(t *testing.T) {
	res, err := SyncDeviceProjection(nil, []DeviceState{{NodeID: "1", Hostname: "x"}})
	if err != nil || res.Written != 0 {
		t.Errorf("nil DB: res=%+v err=%v, want a silent no-op", res, err)
	}
}
