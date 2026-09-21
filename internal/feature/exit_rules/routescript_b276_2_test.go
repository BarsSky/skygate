// routescript_b276_2_test.go — pure-function tests for the
// per-exit-node script generators (B276.2).
//
// The body builders are pure: they take a []ScriptRouteGroup
// and a `restore` flag, return a string. No DB, no headscale,
// no I/O. That's the only piece we can exercise without a
// live tailnet — but it's also the part that got rewritten,
// so it's the one that needs a regression guard.
//
// B276.2 (2026-09-21): the pre-fix script took the FIRST exit
// node from headscale and routed EVERY rule through it,
// regardless of the rule's exit_node field. The new shape is
// per-exit-node groups: each rule's traffic is routed through
// ITS declared exit node, and rules with empty exit_node fold
// into the user's preferred (fallback: first_healthy).

package exit_rules

import (
	"strings"
	"testing"
)

// TestBuildLinux_PerExitNodeBlocks is the headline B276.2
// contract: a user with rules on TWO different exit nodes
// gets one `ip route add …` block per group, each pointing at
// the matching Tailscale IP. Pre-fix this test would have
// failed because both rules landed in a single block with
// the FIRST exit node's IP.
func TestBuildLinux_PerExitNodeBlocks(t *testing.T) {
	groups := []ScriptRouteGroup{
		{
			ExitNode:   "karolina",
			ExitNodeIP: "100.64.0.5",
			Source:     "preferred",
			Routes: []routeEntry{
				{targetType: "subnet", targetVal: "104.16.0.0/12"},
			},
		},
		{
			ExitNode:   "emilia",
			ExitNodeIP: "100.64.0.7",
			Source:     "explicit",
			Routes: []routeEntry{
				{targetType: "ip", targetVal: "8.8.8.8"}, // becomes /32
				{targetType: "subnet", targetVal: "8.8.4.0/24"},
			},
		},
	}
	out := buildLinuxRouteScript(groups, false)
	mustContain(t, out, "=== exit-node: emilia (explicit) ===")
	mustContain(t, out, "=== exit-node: karolina (preferred) ===")
	mustContain(t, out, "ip route add 104.16.0.0/12 via 100.64.0.5")
	mustContain(t, out, "ip route add 8.8.8.8/32 via 100.64.0.7")
	mustContain(t, out, "ip route add 8.8.4.0/24 via 100.64.0.7")
	// DNS route goes via the PREFERRED group, not the alphabetical first.
	mustContain(t, out, "ip route add 100.100.100.100/32 via 100.64.0.5")
}

// TestBuildLinux_AutoRulesFoldIntoPreferred is the other
// B276.2 contract: rules whose exit_node is empty (engine
// auto-pick) are NOT routed through the first healthy
// exit-node in the list — they fold into the user's
// preferred exit node.
func TestBuildLinux_AutoRulesFoldIntoPreferred(t *testing.T) {
	groups := []ScriptRouteGroup{
		{
			ExitNode:   "emilia",
			ExitNodeIP: "100.64.0.7",
			Source:     "explicit",
			Routes: []routeEntry{
				{targetType: "ip", targetVal: "1.1.1.1"},
			},
		},
		{
			// This is the auto group: it has the user's
			// preferred name + Source="preferred" because
			// loadRoutesForScriptGroups folds empty
			// exit_node rules here.
			ExitNode:   "karolina",
			ExitNodeIP: "100.64.0.5",
			Source:     "preferred",
			Routes: []routeEntry{
				{targetType: "subnet", targetVal: "9.9.9.0/24"},
			},
		},
	}
	out := buildLinuxRouteScript(groups, false)
	// The auto rule MUST go via karolina (preferred), not emilia.
	mustContain(t, out, "ip route add 9.9.9.0/24 via 100.64.0.5")
	// And explicitly NOT via emilia.
	if strings.Contains(out, "ip route add 9.9.9.0/24 via 100.64.0.7") {
		t.Fatalf("auto rule routed through emilia instead of the preferred karolina:\n%s", out)
	}
}

// TestBuildLinux_MissingIPPlaceholder ensures that a group
// with no resolved Tailscale IP still emits a recognisable
// block (so the operator can edit by hand) instead of
// emitting `ip route add X via  dev tailscale0` with an
// empty gateway (which would silently succeed as a link
// route to nowhere).
func TestBuildLinux_MissingIPPlaceholder(t *testing.T) {
	groups := []ScriptRouteGroup{
		{
			ExitNode:   "untagged-relay",
			ExitNodeIP: "",
			Source:     "explicit",
			Routes: []routeEntry{
				{targetType: "subnet", targetVal: "9.9.9.0/24"},
			},
		},
	}
	out := buildLinuxRouteScript(groups, false)
	mustContain(t, out, "=== exit-node: untagged-relay (NO TAILSCALE IP")
	if strings.Contains(out, "ip route add 9.9.9.0/24 via  dev") {
		t.Fatalf("emitted a route with empty gateway:\n%s", out)
	}
}

// TestBuildLinux_RestoreClearsAllGroups verifies that the
// restore path emits `ip route del` for every group's IP —
// not just the preferred one. Pre-fix this would have
// silently left the explicit-group routes behind.
func TestBuildLinux_RestoreClearsAllGroups(t *testing.T) {
	groups := []ScriptRouteGroup{
		{
			ExitNode:   "karolina",
			ExitNodeIP: "100.64.0.5",
			Source:     "preferred",
			Routes:     []routeEntry{{targetType: "subnet", targetVal: "104.16.0.0/12"}},
		},
		{
			ExitNode:   "emilia",
			ExitNodeIP: "100.64.0.7",
			Source:     "explicit",
			Routes:     []routeEntry{{targetType: "subnet", targetVal: "8.8.4.0/24"}},
		},
	}
	out := buildLinuxRouteScript(groups, true)
	mustContain(t, out, "ip route del 104.16.0.0/12 via 100.64.0.5")
	mustContain(t, out, "ip route del 8.8.4.0/24 via 100.64.0.7")
}

// TestBuildWindows_PerExitNodeBlocks is the Windows mirror
// of TestBuildLinux_PerExitNodeBlocks. Same per-exit-node
// guarantee; `route add X mask M G` for every route in
// every group.
func TestBuildWindows_PerExitNodeBlocks(t *testing.T) {
	groups := []ScriptRouteGroup{
		{
			ExitNode:   "karolina",
			ExitNodeIP: "100.64.0.5",
			Source:     "preferred",
			Routes: []routeEntry{
				{targetType: "subnet", targetVal: "104.16.0.0/12"},
			},
		},
		{
			ExitNode:   "emilia",
			ExitNodeIP: "100.64.0.7",
			Source:     "explicit",
			Routes: []routeEntry{
				{targetType: "ip", targetVal: "8.8.8.8"},
			},
		},
	}
	out := buildWindowsRouteScript(groups, false)
	mustContain(t, out, "rem === exit-node: emilia (explicit) ===")
	mustContain(t, out, "rem === exit-node: karolina (preferred) ===")
	mustContain(t, out, "route add 104.16.0.0 mask 255.240.0.0 100.64.0.5")
	mustContain(t, out, "route add 8.8.8.8 mask 255.255.255.255 100.64.0.7")
	mustContain(t, out, "route add 100.100.100.100 mask 255.255.255.255 100.64.0.5")
}

// TestPreferredGroup selects the preferred group from a
// heterogeneous list. The body builders rely on this for
// the DNS route and for the restore's default-route re-add.
func TestPreferredGroup(t *testing.T) {
	groups := []ScriptRouteGroup{
		{ExitNode: "emilia", Source: "explicit"},
		{ExitNode: "karolina", Source: "preferred"},
		{ExitNode: "mow", Source: "first_healthy"},
	}
	g := preferredGroup(groups)
	if g == nil || g.ExitNode != "karolina" {
		t.Fatalf("preferredGroup returned %+v, want karolina", g)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("output missing %q\n--- got ---\n%s\n---------------", needle, haystack)
	}
}
