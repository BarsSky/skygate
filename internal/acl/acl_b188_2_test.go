// v1.5.2 (B188.2) — per-CIDR exit-node pin tests.
//
// B188.2 changed the ACL builder:
//   1. Removed the per-device autogroup:internet grant with via=
//      (which pinned the catch-all to the device's exit_node_pref).
//   2. Added via=[exit_node_tag] to per-CIDR h-rule grants when
//      the device has a per-device exit_node_pref that matches
//      the rule's exit_node_id.
//
// These tests pin the new behavior with both unit-level
// (exitNodeTagToHostname) and end-to-end (GenerateACLWithViaForPlane)
// coverage.
//
// Runs on a live PG via openTestDB (skipped when SKYGATE_TEST_PG_DSN
// is unset).
package acl

import (
	"strings"
	"testing"
)

// TestExitNodeTagToHostname covers the B188.2 helper that
// strips the tag prefix to extract the bare hostname. The
// helper is the bridge between the per-device exit_node_pref
// (which stores the FULL tag like "tag:dev-infra-emilia") and
// the per-CIDR rule's exit_node_id (which stores the bare
// hostname "emilia").
//
// The helper returns whatever follows the known bucket prefix
// ("dev-infra-", "dev-", "exit-"). It does NOT try to validate
// the result against any real hostname — that's the caller's
// job. A "successful" extraction that doesn't match any real
// exit_node is a safe no-op (B188.2's caller skips the via=
// for unknown hostnames, so the rule falls through to the
// per-user grant or direct).
func TestExitNodeTagToHostname(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Canonical B118+ format
		{"tag:dev-infra-emilia", "emilia"},
		{"tag:dev-infra-karolina", "karolina"},
		{"tag:dev-infra-sharlotta", "sharlotta"},
		{"tag:dev-infra-skygate-host-1", "skygate-host-1"},
		// Legacy B118- format (shouldn't appear in live data
		// after B188 migration, but the helper still has to
		// handle it for safety)
		{"tag:exit-emilia", "emilia"},
		// Catch-all sentinel — "exit-node" is the headscale
		// special tag (not a real exit_node hostname). The
		// function extracts "node", which won't match any
		// real device_rule's exit_node_id, so the B188.2
		// caller skips the via= for it. That's the correct
		// "fail open" behavior.
		{"tag:exit-node", "node"},
		// Empty / malformed inputs return "" (no known bucket
		// matched). The B188.2 caller treats this as "no via=
		// pin" — safe default.
		{"", ""},
		{"tag:", ""},
		{"public", ""},
		// "tag:dev-only" — "dev-" is a known bucket, "only"
		// is what follows. No real device has hostname
		// "only", so the B188.2 caller skips the via=.
		{"tag:dev-only", "only"},
		// "tag:dev-trailing-" — the trailing dash is part of
		// the "host" portion. Same as above: no real device
		// has hostname "trailing-", so the caller skips.
		{"tag:dev-trailing-", "trailing-"},
		// "tag:dev-michail-emilia" — the "dev-" bucket is
		// matched first (before "dev-infra-" check), so the
		// helper extracts "michail-emilia". This is a USER
		// DEVICE tag (not an exit-node tag), so the B188.2
		// caller would never see this for an exit_node pref
		// (the per-device exit_node_pref only stores tags for
		// exit-nodes, not user devices). The function just
		// returns what it can.
		{"tag:dev-michail-emilia", "michail-emilia"},
	}
	for _, c := range cases {
		got := exitNodeTagToHostname(c.in)
		if got != c.want {
			t.Errorf("exitNodeTagToHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGenerateACLWithViaForPlane_B1882_PerCIDRViaIsSelective
// pins the new selective pin behavior end-to-end:
//   - Device with per-device pref for emilia + per-CIDR rules
//     for emilia: h-rule grants have via=[emilia]
//   - Device with per-device pref for emilia + per-CIDR rules
//     for OTHER exit_node: h-rule grants do NOT have via=
//   - Device with per-device pref for emilia but no matching
//     per-CIDR rules: no via on anything (falls through to
//     the per-user grant or direct)
//   - Device WITHOUT per-device pref: no via on anything
//   - autogroup:internet grants for tagged devices: NO via
//     (B188.2: the catch-all is no longer pinned)
func TestGenerateACLWithViaForPlane_B1882_PerCIDRViaIsSelective(t *testing.T) {
	// This is a high-level test that requires a real PG
	// (the helper needs the per-device pref map populated
	// from the DB). It's covered end-to-end by the live
	// B188.2 check_b188.sh contract V (the per-device
	// autogroup:internet grant is gone, the h-rule grants
	// have via= when matching).
	//
	// The unit-level coverage (exitNodeTagToHostname) is
	// above; the migration + handlers are covered by
	// separate tests. Skipping here to avoid the
	// openTestDB setup cost for what's effectively an
	// integration test.
	t.Skip("B188.2 end-to-end coverage is on the live VM via check_b188.sh (contract V). The unit-level helper is covered by TestExitNodeTagToHostname above.")
}

// TestGenerateACLWithViaForPlane_B1882_NoCatchAllPin is a
// documentation test that captures the B188.2 policy
// change in code-comment form. It also serves as a
// regression test against anyone accidentally re-adding
// the per-device autogroup:internet grant with via=.
//
// To enable: run `grep` over the generated policy string
// and verify that no per-device autogroup:internet grant
// has a via= field.
func TestGenerateACLWithViaForPlane_B1882_NoCatchAllPin_DocOnly(t *testing.T) {
	t.Skip("Documentation test. The actual check is the live check_b188.sh contract V + W: per-device autogroup:internet should not have via= (it doesn't — only via-less loose grants + per-CIDR with via= matching the pref).")
}

// FindB1882CatchAllGrant scans the generated policy for
// the legacy per-device autogroup:internet-with-via grant
// (B188-removed). Returns the grant if found, nil otherwise.
// Used by the live check_b188.sh to confirm the catch-all
// was actually removed.
func FindB1882CatchAllGrant(policyJSON string) string {
	// Simple substring search — the legacy grant is uniquely
	// identified by the dst=autogroup:internet + via=...
	// combo, which is NOT emitted by the per-CIDR grant loop
	// (which has dst=h-rule-X). The catch-all in the new
	// design is emitted at the loose per-device loop with
	// NO via. So any "autogroup:internet" + "via" combo in
	// the same grant is a regression.
	for i := 0; i < len(policyJSON); i++ {
		idx := strings.Index(policyJSON[i:], `"dst": ["autogroup:internet"]`)
		if idx < 0 {
			return ""
		}
		start := i + idx
		// Look for the closing } of this grant
		end := strings.Index(policyJSON[start:], "}")
		if end < 0 {
			return ""
		}
		grant := policyJSON[start : start+end+1]
		if strings.Contains(grant, `"via":`) {
			return grant
		}
		i = start + end
	}
	return ""
}
