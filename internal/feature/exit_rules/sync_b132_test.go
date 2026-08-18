// 2026-08-18 (B132): unit tests for the per-row sync.
//
// The pre-B132 unit tests in this file only covered the
// combinedResultFor pure helper (which mirrors the
// production format string). B132 adds a per-row entry
// point (SyncAdvertisedRoutesForNode) that's the operator's
// main fix for the "mismatch висит" report — it gets called
// when the user clicks the per-row "Re-sync" button on
// /admin/exit-nodes.
//
// The test below pins the contract for the no-DB-needed
// paths: empty hostname, and the "no rules" early return.
// The "with rules" path requires a real headscale.Client
// and is covered by the live verify-pre + audit_log
// regression check (B127+R5 style: every successful deploy
// logs an audit_log entry that grep for ssh=ok/err=).
package exit_rules

import (
	"database/sql"
	"strings"
	"testing"
)

// TestSyncAdvertisedRoutesForNode_EmptyHostname pins that the
// empty-hostname short-circuit returns {"error": "..."} and
// never touches the DB or the headscale client. The pre-B132
// path was a panic-or-silent-failure depending on the
// map's nil state.
func TestSyncAdvertisedRoutesForNode_EmptyHostname(t *testing.T) {
	s := &Service{DB: nil} // DB is nil — the empty path must not touch it
	res := s.SyncAdvertisedRoutesForNode("")
	if res == nil {
		t.Fatalf("result map must not be nil, got nil")
	}
	if v, ok := res["error"]; !ok || !strings.Contains(v, "empty") {
		t.Errorf("expected 'error' key with 'empty' in value, got %v", res)
	}
}

// TestSyncAdvertisedRoutesForNode_NoRules pins the "node
// has no enabled IP/subnet rules" early return. Used when
// the operator clicks "Re-sync" on a node that has no
// skygate rules targeting it (e.g. a relay that was just
// added and not yet referenced by any device_rule). The
// expected result is a single-entry map with the
// "info=no rules" message — no ssh call, no headscale
// approve call.
func TestSyncAdvertisedRoutesForNode_NoRules(t *testing.T) {
	// Use a fresh in-memory SQLite DB so the SELECT runs
	// against an empty device_rules table. We don't need
	// the full openTestDB helper (which sets up the
	// migration chain) because the per-row SELECT is a
	// one-liner against a single table.
	dbFile := t.TempDir() + "/b132.db"
	d, err := sql.Open("sqlite3", "file:"+dbFile+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Skipf("sqlite3 driver not available in this build: %v", err)
		return
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE device_rules (
		id INTEGER PRIMARY KEY,
		exit_node_id TEXT,
		target_type TEXT,
		target_value TEXT,
		enabled INTEGER DEFAULT 1
	)`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	s := &Service{DB: d}
	res := s.SyncAdvertisedRoutesForNode("emilia")
	if v, ok := res["emilia"]; !ok || !strings.Contains(v, "no IP/subnet rules") {
		t.Errorf("expected emilia=info=no rules, got %v", res)
	}
	// No error key (the absence-of-rules case is a normal
	// flow, not an error).
	if _, ok := res["error"]; ok {
		t.Errorf("did not expect 'error' key on empty-rules path, got %v", res)
	}
}
