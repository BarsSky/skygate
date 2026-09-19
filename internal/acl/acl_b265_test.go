// File: internal/acl/acl_b265_test.go
//
// B265 (2026-09-19) — the per-device exit-node PIN and the
// node_owner_map src fallback.
//
// Two live-verified defects are pinned here:
//
//  1. Nothing in the generated grants[] policy constrained WHICH exit
//     node a TAGGED device may use. headscale honours `via` for
//     exit-node selection only when the grant's dst contains
//     `autogroup:internet`, and the only such grant carrying `via` was
//     the per-USER grant (src=user@…), which headscale never matches
//     against a tagged node (every skygate device carries
//     tag:dev-<user>-<device>). The per-CIDR grants do carry `via`, but
//     with dst=h-rule-*, which headscale treats as subnet-route
//     steering only. B265 emits a per-device
//     `dst=[autogroup:internet], via=[<pref>]` grant when — and only
//     when — the device has a resolved preference, replacing the loose
//     unpinned grant for that device (grants are additive: emitting both
//     would leave the pin defeated).
//
//  2. device_rules rows with an empty denormalised user_name (173 of
//     328 live) emitted `src: ["100.64.0.x"]` instead of the device tag.
//     A raw device-IP src is a different selector than the tag, so it
//     cancels headscale's exit-route exclusion for that prefix
//     (ViaRoutesForPeer) AND dies when the node's tailnet IP changes.
//     B265 resolves the owner from node_owner_map via the rule's
//     device_id.
//
// The grant-shape tests run against a real database via
// db.OpenTestPG (skipped when SKYGATE_TEST_PG_DSN is unset), so they
// exercise the real generator against the real (empty, per-test) schema.

package acl

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

// seedB265Schema creates the minimum rows the grants generator reads:
// one portal user, one node_owner_map row (owner + hostname), the
// per-user own subnet, and a device_exit_node_prefs row carrying the
// preference.
func seedB265Schema(t *testing.T, conn *sql.DB) {
	t.Helper()
	d := conn
	// portal_users: id 1 "tester" linked to headscale user 1.
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
		VALUES (1, 'tester', 'x', 0, 1)`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	// node_owner_map: node 101 "workstation" owned by tester.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
		VALUES ('101', 1, 'tester', 'tag:dev-tester-workstation', 1, 0, 'workstation')`); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
	// node_owner_map: node 102 "relay-1" — the exit node.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
		VALUES ('102', 1, 'infra', 'tag:dev-infra-relay-1', 1, 0, 'relay-1')`); err != nil {
		t.Fatalf("seed node_owner_map (relay): %v", err)
	}
	// The user's own subnet (drives the h-user-*-subnet alias).
	if _, err := d.Exec(`INSERT INTO user_subnets (user_id, cidr, status) VALUES (1, '10.0.1.0/24', 'active')`); err != nil {
		t.Logf("seed user_subnets skipped: %v", err)
	}
}

// countGrants prints / counts grants matching a predicate.
func countGrantsText(t *testing.T, policy, src, dstContains string, mustHaveVia bool) int {
	t.Helper()
	n := 0
	for _, g := range parseGrantsForTest(t, policy) {
		if !strings.Contains(g, `"src": ["`+src+`"]`) {
			continue
		}
		if !strings.Contains(g, dstContains) {
			continue
		}
		if mustHaveVia && !strings.Contains(g, `"via":`) {
			continue
		}
		if !mustHaveVia && strings.Contains(g, `"via":`) {
			continue
		}
		n++
	}
	return n
}

// parseGrantsForTest splits the grants array into one string per grant.
// Deliberately dumb (no JSON dependency on purpose: the file already
// imports what it needs and the generator emits one grant per line).
func parseGrantsForTest(t *testing.T, policy string) []string {
	t.Helper()
	start := strings.Index(policy, `"grants": [`)
	if start < 0 {
		t.Fatalf("policy has no grants array:\n%s", policy)
	}
	body := policy[start:]
	end := strings.Index(body, "\n  ],")
	if end < 0 {
		t.Fatalf("policy grants array is not terminated:\n%s", policy)
	}
	body = body[:end]
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{ ") {
			out = append(out, line)
		}
	}
	return out
}

// TestB265_PerDevicePin_WhenPreferenceSet is the load-bearing test:
// a device WITH a pref gets exactly one pinned autogroup:internet grant
// and NO loose unpinned one (additive grants would defeat the pin).
func TestB265_PerDevicePin_WhenPreferenceSet(t *testing.T) {
	conn := db.OpenTestPG(t)
	seedB265Schema(t, conn)
	d := conn
	if _, err := d.Exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (1, 'workstation', 'tag:dev-infra-relay-1', 1, 0, 1)`); err != nil {
		t.Fatalf("seed device_exit_node_prefs: %v", err)
	}

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	pinned := countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, true)
	loose := countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, false)
	if pinned != 1 {
		t.Errorf("pinned autogroup:internet grants for tag:dev-tester-workstation = %d, want 1 (B265 per-device exit-node pin)", pinned)
	}
	if loose != 0 {
		t.Errorf("loose (unpinned) autogroup:internet grants for tag:dev-tester-workstation = %d, want 0 — grants are additive, so a loose grant defeats the pin", loose)
	}
	if !strings.Contains(policy, `"via": ["tag:dev-infra-relay-1"]`) {
		t.Errorf("policy does not carry the expected via tag:\n%s", policy)
	}
}

// TestB265_PerDevicePin_AbsentWhenNoPreference pins the "do not take
// away the device's own choice" half: without a preference the loose
// grant must remain (this is the global rule the operator relies on when
// a device explicitly routes everything through an exit node).
func TestB265_PerDevicePin_AbsentWhenNoPreference(t *testing.T) {
	conn := db.OpenTestPG(t)
	seedB265Schema(t, conn)
	d := conn

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	if got := countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, true); got != 0 {
		t.Errorf("pinned autogroup:internet grants = %d, want 0 when the device has no preference", got)
	}
	if got := countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, false); got != 1 {
		t.Errorf("loose autogroup:internet grants = %d, want 1 (the global fallback must survive)", got)
	}
}

// TestB265_RuleSrcPrefersTagOverDeviceIP pins defect 2: a rule whose
// denormalised user_name/device_hostname are empty must still emit the
// device TAG as src, resolved from node_owner_map by device_id — not the
// device IP.
func TestB265_RuleSrcPrefersTagOverDeviceIP(t *testing.T) {
	conn := db.OpenTestPG(t)
	seedB265Schema(t, conn)
	d := conn
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, user_name, device_hostname, enabled)
		VALUES (1, 101, 'relay-1', 'subnet', '1.2.3.0/24', 'accept', '100.64.0.14', '', '', 1)`); err != nil {
		t.Fatalf("seed device_rules: %v", err)
	}

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	if !strings.Contains(policy, `"src": ["tag:dev-tester-workstation"]`) {
		t.Errorf("rule with empty user_name/device_hostname did not resolve to the device tag via node_owner_map:\n%s", policy)
	}
	if strings.Contains(policy, `"src": ["100.64.0.14"]`) {
		t.Errorf("rule still emits the raw device IP as src — that selector cancels the via exit-node pin and breaks on re-registration:\n%s", policy)
	}
}

// TestDeviceTagForRule_Pure exercises the resolver directly (no DB):
// denormalised columns win, node_owner_map fills the gaps, and an
// unresolvable rule yields "" (the caller then falls back to device_ip).
func TestDeviceTagForRule_Pure(t *testing.T) {
	owners := map[int]deviceOwner{
		101: {Username: "tester", Hostname: "workstation"},
		102: {Username: "Infra", Hostname: "Relay-1"},
	}
	cases := []struct {
		name string
		in   db.ACLEntry
		want string
	}{
		{"denormalised columns win", db.ACLEntry{UserName: "tester", DeviceHostname: "Workstation", DeviceID: 101}, "tag:dev-tester-workstation"},
		{"resolved from owner map", db.ACLEntry{DeviceID: 101}, "tag:dev-tester-workstation"},
		{"hostname only is filled from map", db.ACLEntry{UserName: "tester", DeviceID: 101}, "tag:dev-tester-workstation"},
		{"username and hostname are lowercased from the map", db.ACLEntry{DeviceID: 102}, "tag:dev-infra-relay-1"},
		{"unresolvable yields empty", db.ACLEntry{DeviceID: 999}, ""},
		{"no id and no columns yields empty", db.ACLEntry{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deviceTagForRule(c.in, owners); got != c.want {
				t.Errorf("deviceTagForRule(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
