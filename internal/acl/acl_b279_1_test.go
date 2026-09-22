// acl_b279_1_test.go — B279.1 (v1.5.46) — the ACL package's tag→hostname
// helper must agree with the shared class-tag predicate.
//
// WHY THIS FILE EXISTS
// --------------------
// `exitNodeTagToHostname` was one of four copies of "strip `tag:` to get a
// hostname" (the others: internal/feature/exit_rules.TagToHostname,
// internal/db/exit_node_prefs.go's form check, and an inline guard in
// internal/feature/admin/user_subnet.go). This copy was the SAFE one — it
// returned "node" for `tag:exit-node` and its callers were documented to
// treat that as a non-match — but "safe because the caller read a comment"
// is not a mechanism. The live `aro` incident happened in the sibling copy
// that had no comment: the class tag became the hostname `node`, was written
// into 23 device_rules rows, and routes were advertised/approved for a relay
// that does not exist.
//
// B279.1 removes the duplication: the class-tag knowledge comes from
// db.IsClassTag (internal/db/tag_kind.go) in every copy, and this test pins
// that the ACL helper cannot drift from the predicate again.
package acl

import (
	"testing"

	"skygate/internal/db"
)

// TestExitNodeTagToHostname_ClassTagsAreNotHostnames_B279_1 pins the
// sentinel behaviour: every class tag yields "" (the callers' "no via= pin"
// signal) instead of a hostname.
func TestExitNodeTagToHostname_ClassTagsAreNotHostnames_B279_1(t *testing.T) {
	for _, class := range []string{
		"tag:exit-node",
		"tag:public",
		"tag:private",
		"tag:subnet-router",
		"TAG:Exit-Node",
		"  tag:public  ",
	} {
		if got := exitNodeTagToHostname(class); got != "" {
			t.Errorf("exitNodeTagToHostname(%q) = %q, want \"\" (a class tag names a role, not a node — %q is exactly the phantom that was written into 23 rules)", class, got, got)
		}
	}
	// Per-node forms are untouched.
	perNode := map[string]string{
		"tag:dev-infra-exit-node-vps": "exit-node-vps",
		"tag:dev-infra-emilia":        "emilia",
		"tag:exit-emilia":             "emilia",
		"tag:dev-michail-emilia":      "michail-emilia",
	}
	for in, want := range perNode {
		if got := exitNodeTagToHostname(in); got != want {
			t.Errorf("exitNodeTagToHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExitNodeTagToHostname_MatchesSharedPredicate_B279_1 is the
// anti-drift guard: no class tag may ever produce a hostname here, whatever
// the shared predicate considers a class.
//
// It also pins the other direction, which is what makes the guard
// meaningful: a value the predicate does NOT consider a class tag must not
// be silently swallowed (the legacy per-node form still resolves), so a
// future "just return empty for everything starting with tag:exit" shortcut
// fails here.
func TestExitNodeTagToHostname_MatchesSharedPredicate_B279_1(t *testing.T) {
	all := []string{
		"tag:exit-node", "tag:public", "tag:private", "tag:subnet-router",
		"tag:dev-infra-emilia", "tag:exit-emilia", "tag:dev-michail-emilia",
		"tag:", "", "public", "emilia",
	}
	for _, tag := range all {
		got := exitNodeTagToHostname(tag)
		if db.IsClassTag(tag) && got != "" {
			t.Errorf("db.IsClassTag(%q) = true but exitNodeTagToHostname(%q) = %q — the acl copy drifted from the shared predicate", tag, tag, got)
		}
	}
	if got := exitNodeTagToHostname("tag:exit-emilia"); got != "emilia" {
		t.Errorf("legacy per-node form regressed: exitNodeTagToHostname(\"tag:exit-emilia\") = %q, want \"emilia\"", got)
	}
}

// TestResolvePerCIDRVia_ClassTagPrefIsNotAPin_B279_1 pins the caller's
// outcome, which is what actually protects traffic: a device whose stored
// preference is a class tag gets NO via= pin (fail open), instead of a pin
// naming a tag nobody carries — a `via` on a CIDR grant is a permission
// FILTER, so a pin that matches no node silently blocks those destinations
// (the B276/L-10.9 failure mode).
func TestResolvePerCIDRVia_ClassTagPrefIsNotAPin_B279_1(t *testing.T) {
	via := map[string]string{"tag:dev-michail-basic": "tag:exit-node"}
	if got := resolvePerCIDRVia("tag:dev-michail-basic", "exit-node-vps", via); got != "" {
		t.Errorf("resolvePerCIDRVia with a class-tag preference = %q, want \"\" (no pin)", got)
	}
	// Sanity: a real per-node preference still pins.
	via["tag:dev-michail-basic"] = "tag:dev-infra-exit-node-vps"
	if got := resolvePerCIDRVia("tag:dev-michail-basic", "exit-node-vps", via); got != "tag:dev-infra-exit-node-vps" {
		t.Errorf("resolvePerCIDRVia with a per-node preference = %q, want tag:dev-infra-exit-node-vps", got)
	}
}
