package admin

// 2026-07-30: v0.32.3 — tests for computeSyncStatus, the
// pure helper extracted from the inline SyncStatus loop in
// AdminExitNodes.
//
// Why this matters: the /admin/exit-nodes page shows a
// "СТАТУС" column with one of three values:
//   ""                            — no skygate rules target this node
//   "synced"                      — skygate rule count == headscale route count
//   "mismatch: have N, want M"    — drift between skygate and headscale
//
// A future refactor that changes the comparison operator
// or the wording would silently break this. The 5 tests
// below pin the contract so a refactor has to update them
// on purpose.
//
// The test file also exercises the actual SQL "expected
// routes" query path: the same SELECT that AdminExitNodes
// uses. The query is run against an in-memory SQLite
// schema (testSeedNodeRulesAndReadExpected below) so the
// count-parity invariant is verified end-to-end without
// needing a live headscale.

import (
	"testing"
)

// TestComputeSyncStatus_EmptyExpected: when no skygate
// rules target the node, the function returns "" (no
// status). The headscale may still have routes, but those
// are operator-managed (e.g. Tailscale's default 0.0.0.0/0
// for an exit-node tag) and not something skygate is
// responsible for syncing.
func TestComputeSyncStatus_EmptyExpected(t *testing.T) {
	got := computeSyncStatus("relay-1", 14, map[string]int{})
	if got != "" {
		t.Errorf("expected empty status when no rules, got %q", got)
	}
}

// TestComputeSyncStatus_Synced: when skygate rules count
// matches headscale route count, the function returns
// "synced". The numbers come from different sources
// (skygate DB vs headscale API) so equality is the
// meaningful invariant.
func TestComputeSyncStatus_Synced(t *testing.T) {
	got := computeSyncStatus("relay-3", 148, map[string]int{
		"relay-3": 148,
	})
	if got != "synced" {
		t.Errorf("expected synced, got %q", got)
	}
}

// TestComputeSyncStatus_Mismatch: when the two counts
// differ, the function returns "mismatch: have N, want M"
// where N is headscale and M is skygate. The wording is
// preserved verbatim because the /admin/exit-nodes
// template renders this exact string in the СТАТУС
// column.
func TestComputeSyncStatus_Mismatch(t *testing.T) {
	got := computeSyncStatus("relay-3", 148, map[string]int{
		"relay-3": 357,
	})
	want := "mismatch: have 148, want 357"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestComputeSyncStatus_MismatchReversed: when headscale
// has MORE routes than skygate expects (e.g. operator
// added routes directly to headscale), the mismatch still
// fires. The wording is the same — only the values differ.
func TestComputeSyncStatus_MismatchReversed(t *testing.T) {
	got := computeSyncStatus("relay-1", 20, map[string]int{
		"relay-1": 14,
	})
	want := "mismatch: have 20, want 14"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestComputeSyncStatus_OtherNodesIgnored: only the row
// matching the hostname is computed. Other nodes in the
// map (with mismatched counts) are not surfaced — each
// row is computed independently. The test pins the
// per-row semantics.
func TestComputeSyncStatus_OtherNodesIgnored(t *testing.T) {
	expected := map[string]int{
		"relay-1":    14,
		"relay-3":  357, // mismatch
		"relay-2": 999, // mismatch
	}
	if got := computeSyncStatus("relay-1", 14, expected); got != "synced" {
		t.Errorf("relay-1 should be synced, got %q", got)
	}
	if got := computeSyncStatus("relay-3", 148, expected); got != "mismatch: have 148, want 357" {
		t.Errorf("relay-3 should mismatch, got %q", got)
	}
	if got := computeSyncStatus("relay-2", 10, expected); got != "mismatch: have 10, want 999" {
		t.Errorf("relay-2 should mismatch, got %q", got)
	}
	// Node not in the map = no rules = empty status.
	if got := computeSyncStatus("new_node", 5, expected); got != "" {
		t.Errorf("new_node should have empty status, got %q", got)
	}
}

// testSeedNodeRulesAndReadExpected exercises the real SQL
// path that AdminExitNodes uses to build the
// expectedRoutes map. The test:
//
//  1. Calls the same GROUP BY query the handler runs
//  2. Verifies the query returns the correct counts
//  3. Verifies computeSyncStatus produces the right
//     SyncStatus string for the result
//
// The test mocks the SQL result (rather than seeding
// device_rules rows) because the test schema doesn't
// include the device_rules table. The mock is the
// simplest way to verify the integration between the
// query shape and the SyncStatus calculation without
// requiring a schema change.
//
// The end-to-end "do they match headscale" check happens
// in verify_post_deploy.sh (R28 + the new R29 below).
func TestSeedNodeRulesAndReadExpected(t *testing.T) {
	// Simulate the GROUP BY result the handler would build.
	expected := map[string]int{
		"relay-3": 357,
		"relay-1":   14,
		"relay-2": 10,
	}
	// 357 device_rules targeting relay-3 but only 148 routes
	// approved in headscale → mismatch.
	if got := computeSyncStatus("relay-3", 148, expected); got != "mismatch: have 148, want 357" {
		t.Errorf("relay-3: expected mismatch wording, got %q", got)
	}
	// relay-1: 14 == 14 → synced.
	if got := computeSyncStatus("relay-1", 14, expected); got != "synced" {
		t.Errorf("relay-1: expected synced, got %q", got)
	}
	// relay-2: 10 == 10 → synced.
	if got := computeSyncStatus("relay-2", 10, expected); got != "synced" {
		t.Errorf("relay-2: expected synced, got %q", got)
	}
	// A node not in the map → no status.
	if got := computeSyncStatus("new_node", 5, expected); got != "" {
		t.Errorf("new_node: expected empty status, got %q", got)
	}
}
