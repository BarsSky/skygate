// v1.5.2 (B188.2) — per-CIDR exit-node pin unit tests.
//
// The per-CIDR via= logic in GenerateACLWithViaForPlane is
// covered end-to-end by:
//   * scripts/check_b188_2.sh contracts S-X (live policy
//     checks on the VM).
//   * The TestExitNodeTagToHostname unit test below (the
//     tag-stripping helper that bridges between the per-device
//     pref's full tag and the per-CIDR rule's hostname).
//
// We don't repeat the end-to-end coverage as a Go unit test
// (it would require a full openTestDB + a complex seeded
// dataset, duplicating the live check's value with none of
// its up-to-date-ness). The B-check script is the right
// place for the end-to-end contract.

package acl

import "testing"

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
