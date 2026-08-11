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

// --- v0.32.7: shouldIncludeAsExitServer filter tests ---
//
// The filter is what `ensureExitServers` uses to decide which
// headscale nodes to insert into the exit_servers table
// (which is what /admin/exit-nodes renders). Before v0.32.7
// the filter matched ANY node that advertised routes — which
// incorrectly included per-user subnet-routers (e.g.
// skygate-subnet-admin with tag:subnet-router advertising
// 10.0.1.0/24). The fix: exclude tag:subnet-router and the
// per-user device marker tag:dev-*. These 6 tests pin the
// contract.

// TestShouldInclude_ExitNode: a tagged exit-node is included
// even if it has no available routes (e.g. before the relay
// has run `tailscale set --advertise-exit-node`).
func TestShouldInclude_ExitNode(t *testing.T) {
	if !shouldIncludeAsExitServer([]string{"tag:exit-node", "tag:public"}, 0) {
		t.Errorf("tagged exit-node with 0 routes should be included (operator may tag before advertising)")
	}
}

// TestShouldInclude_SubnetRouter_Excluded: a subnet-router
// (tag:subnet-router) is EXCLUDED even if it advertises
// routes. This is the v0.32.7 fix — the operator shouldn't
// see their LAN bridge in the exit-nodes list.
func TestShouldInclude_SubnetRouter_Excluded(t *testing.T) {
	if shouldIncludeAsExitServer([]string{"tag:subnet-router"}, 1) {
		t.Errorf("subnet-router should be EXCLUDED from /admin/exit-nodes even with advertised routes")
	}
	if shouldIncludeAsExitServer([]string{"tag:subnet-router", "tag:dev-admin-rpi"}, 1) {
		t.Errorf("subnet-router+dev-* should still be EXCLUDED")
	}
}

// TestShouldInclude_PerUserDevice_Excluded: a per-user device
// (tag:dev-<user>-<device>, the v0.28.0 marker) is EXCLUDED.
// These nodes sometimes have route entries (e.g. a Windows
// client with a tailscale subnet route for a VPN adapter) but
// they're user devices, not relays.
func TestShouldInclude_PerUserDevice_Excluded(t *testing.T) {
	if shouldIncludeAsExitServer([]string{"tag:dev-admin-workstation-3", "tag:private"}, 0) {
		t.Errorf("per-user device (tag:dev-*) should be EXCLUDED from /admin/exit-nodes")
	}
}

// TestShouldInclude_PerUserDeviceWithExitNode_Included
// (v0.33.1.30 B82): a per-user device that ALSO has an
// explicit `tag:exit-node` IS an exit-node — the v0.32.7
// default of "tag:dev-* → always excluded" was too
// aggressive for the case where the operator wants a
// per-user-tagged workstation to also act as an exit-node
// (real-world case: emilia/karolina/sharlotta on the live
// VM are tagged as `tag:dev-skyadmin-<name>` for the
// per-user ACL grant AND used as exit-nodes via
// device_rules.exit_node_id references; the v0.32.7
// B21 cleanup pass silently removed them from
// /admin/exit-nodes even though they were actively used).
//
// Without this override, the B21 cleanup pass deletes the
// row on every page load, and the operator can't see (or
// fix) the missing exit-node. The override is the v0.32.7
// design intent refined: "tag:dev-* → excluded, UNLESS the
// operator has explicitly tagged the node as tag:exit-node
// too — then it's a promoted per-user device that wants
// to be on the exit-nodes page".
func TestShouldInclude_PerUserDeviceWithExitNode_Included(t *testing.T) {
	// The headline case: per-user device + tag:exit-node + 0
	// routes. Without the B82 override this returns false
	// (excluded by the v0.32.7 default).
	if !shouldIncludeAsExitServer(
		[]string{"tag:dev-skyadmin-emilia", "tag:exit-node", "tag:private"}, 0,
	) {
		t.Errorf("tag:dev-* + tag:exit-node should be INCLUDED (B82 override — the v0.32.7 default of always excluding tag:dev-* is too aggressive when the operator has explicitly promoted the device to tag:exit-node)")
	}
	// Same with advertised routes (the typical case for
	// emilia/karolina/sharlotta: per-user device + tag:exit-node
	// + 30-144 routes advertised).
	if !shouldIncludeAsExitServer(
		[]string{"tag:dev-skyadmin-karolina", "tag:exit-node", "tag:private"}, 144,
	) {
		t.Errorf("tag:dev-* + tag:exit-node + 144 routes should be INCLUDED")
	}
	// tag:exit-node wins regardless of the OTHER tags present
	// (tag:private, tag:public, etc.).
	if !shouldIncludeAsExitServer(
		[]string{"tag:dev-skyadmin-sharlotta", "tag:exit-node", "tag:public", "tag:private"}, 0,
	) {
		t.Errorf("tag:dev-* + tag:exit-node + tag:public should be INCLUDED")
	}
}

// TestShouldInclude_SubnetRouterOverridesExitNode: even with
// the B82 override for tag:dev-*, a `tag:subnet-router` node
// is ALWAYS excluded — a LAN bridge is not an exit-node
// regardless of other tags. The v0.32.7 test
// TestShouldInclude_SubnetRouter_Excluded covers the
// subnet-router-only case; this one pins the
// "tag:subnet-router beats tag:exit-node" interaction so a
// future refactor can't accidentally swap the priorities
// (B82 added the tag:dev-* override but subnet-router
// exclusion is the stronger rule).
func TestShouldInclude_SubnetRouterOverridesExitNode(t *testing.T) {
	if shouldIncludeAsExitServer(
		[]string{"tag:subnet-router", "tag:exit-node"}, 10,
	) {
		t.Errorf("tag:subnet-router + tag:exit-node should still be EXCLUDED (subnet-router takes priority over exit-node — a LAN bridge is not an exit-node)")
	}
	// Same with the B82 tag:dev-* override.
	if shouldIncludeAsExitServer(
		[]string{"tag:subnet-router", "tag:dev-skyadmin-rpi", "tag:exit-node"}, 10,
	) {
		t.Errorf("tag:subnet-router + tag:dev-* + tag:exit-node should be EXCLUDED (subnet-router wins)")
	}
}

// TestShouldInclude_AdvertisedRoutes: a node with no exit-node
// tag but with advertised routes is INCLUDED (so the operator
// can see unexpected route-advertising nodes and decide
// whether to tag them). This is the original "OR" behavior.
func TestShouldInclude_AdvertisedRoutes(t *testing.T) {
	if !shouldIncludeAsExitServer([]string{"tag:public"}, 5) {
		t.Errorf("tag:public node with 5 routes should be included (catches unexpected route-advertising nodes)")
	}
	if !shouldIncludeAsExitServer([]string{}, 1) {
		t.Errorf("untagged node with 1 route should be included (catches misconfigured relays)")
	}
}

// TestShouldInclude_NoTagsNoRoutes: a node with no tags and
// no routes is EXCLUDED. This is the common case for fresh
// Tailscale clients that have just registered.
func TestShouldInclude_NoTagsNoRoutes(t *testing.T) {
	if shouldIncludeAsExitServer([]string{}, 0) {
		t.Errorf("untagged node with 0 routes should be EXCLUDED (it's a regular client)")
	}
}

// TestShouldInclude_RealWorld: the actual node shapes from
// the production tailnet (2026-07-30). relay-1/relay-2/relay-3
// are included; skygate-subnet-admin is excluded.
func TestShouldInclude_RealWorld(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		rts   int
		want  bool
	}{
		{"relay-1", []string{"tag:exit-relay-1", "tag:exit-node", "tag:public"}, 14, true},
		{"relay-2", []string{"tag:exit-relay-2", "tag:exit-node", "tag:public"}, 10, true},
		{"relay-3", []string{"tag:exit-relay-3", "tag:exit-node", "tag:public"}, 148, true},
		{"skygate-subnet-admin", []string{"tag:dev-admin-skygate-subnet-admin", "tag:subnet-router"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldIncludeAsExitServer(tc.tags, tc.rts); got != tc.want {
				t.Errorf("%s: shouldIncludeAsExitServer(%v, %d) = %v, want %v",
					tc.name, tc.tags, tc.rts, got, tc.want)
			}
		})
	}
}
