// approved_routes_index_b_test.go — pure-function tests for
// indexNodesApprovedRoutes.
//
// The function builds the hostname → approved-routes map
// used by both /my/exit-rules and /admin/exit-rules. The pre-fix
// version indexed the map by `n.GivenName` first (with Hostname
// fallback), which silently broke the look-up whenever headscale
// returned `GivenName != Hostname` (a node with a MagicDNS
// suffix — GivenName="node.tail-scale.ts.net", Hostname="node").
// The rule's exit_node_id is ALWAYS `n.Hostname`, so the look-up
// miss left every rule in `pending` even after SyncAdvertisedRoutes
// had successfully pushed the routes to headscale.
//
// These tests pin the BOTH-name indexing behaviour.

package exit_rules

import (
	"reflect"
	"testing"

	"skygate/internal/headscale"
)

// TestIndexNodesApprovedRoutes_BothNames pins the headline
// regression guard: a node with GivenName="node.tail-scale.ts.net"
// and Hostname="node" must be reachable under BOTH keys.
func TestIndexNodesApprovedRoutes_BothNames(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:       "node",
			GivenName:       "node.tail-scale.ts.net",
			ApprovedRoutes:  []string{"104.16.0.0/12", "8.8.8.8/32"},
			IsExitNode:      true,
		},
	}
	got := indexNodesApprovedRoutes(nodes)

	// Both keys must be present and have the same set.
	for _, key := range []string{"node", "node.tail-scale.ts.net"} {
		set, ok := got[key]
		if !ok {
			t.Fatalf("approvedByExitNode[%q] missing", key)
		}
		if !set["104.16.0.0/12"] || !set["8.8.8.8/32"] {
			t.Errorf("approvedByExitNode[%q] = %v; want both 104.16.0.0/12 and 8.8.8.8/32", key, set)
		}
	}
}

// TestIndexNodesApprovedRoutes_OnlyHostname pins the case
// where headscale returns GivenName="" (older deployments, or
// nodes that haven't been named yet). The map should still be
// indexed under Hostname.
func TestIndexNodesApprovedRoutes_OnlyHostname(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:      "karolina",
			GivenName:      "",
			ApprovedRoutes: []string{"1.1.1.0/24"},
		},
	}
	got := indexNodesApprovedRoutes(nodes)
	if _, ok := got["karolina"]; !ok {
		t.Fatalf("approvedByExitNode[\"karolina\"] missing when GivenName is empty")
	}
	// The empty GivenName should NOT appear as a key.
	if _, ok := got[""]; ok {
		t.Errorf("approvedByExitNode has an empty-string key: %v", got)
	}
}

// TestIndexNodesApprovedRoutes_OnlyGivenName is the inverse:
// a headscale response where Hostname="" and GivenName="realname"
// should still index under the GivenName (so look-ups by the
// bare name succeed).
func TestIndexNodesApprovedRoutes_OnlyGivenName(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:      "",
			GivenName:      "realname",
			ApprovedRoutes: []string{"2.2.2.2/32"},
		},
	}
	got := indexNodesApprovedRoutes(nodes)
	if _, ok := got["realname"]; !ok {
		t.Fatalf("approvedByExitNode[\"realname\"] missing when Hostname is empty")
	}
}

// TestIndexNodesApprovedRoutes_NoApprovedRoutes pins the
// "skip nodes without ApprovedRoutes" path. The map must NOT
// contain the hostname key in that case (otherwise the rule's
// look-up returns an empty set, which is the same `pending`
// state we are trying to fix).
func TestIndexNodesApprovedRoutes_NoApprovedRoutes(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:      "no-routes",
			GivenName:      "no-routes.tail-scale.ts.net",
			ApprovedRoutes: nil,
		},
	}
	got := indexNodesApprovedRoutes(nodes)
	if len(got) != 0 {
		t.Fatalf("approvedByExitNode = %v; want empty map", got)
	}
}

// TestIndexNodesApprovedRoutes_MultipleNodes pins the multi-node
// path: each node contributes its own entry (and its own alias
// under the BOTH-names indexing).
func TestIndexNodesApprovedRoutes_MultipleNodes(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:      "karolina",
			GivenName:      "karolina.tail-scale.ts.net",
			ApprovedRoutes: []string{"1.1.1.0/24"},
		},
		{
			Hostname:      "emilia",
			GivenName:      "emilia.tail-scale.ts.net",
			ApprovedRoutes: []string{"2.2.2.0/24"},
		},
	}
	got := indexNodesApprovedRoutes(nodes)
	if len(got) != 4 { // 2 nodes × 2 names
		t.Fatalf("len(approvedByExitNode) = %d; want 4 (got map = %v)", len(got), got)
	}
	if !got["karolina"]["1.1.1.0/24"] {
		t.Errorf("karolina missing 1.1.1.0/24")
	}
	if !got["karolina.tail-scale.ts.net"]["1.1.1.0/24"] {
		t.Errorf("karolina.tail-scale.ts.net missing 1.1.1.0/24")
	}
	if !got["emilia"]["2.2.2.0/24"] {
		t.Errorf("emilia missing 2.2.2.0/24")
	}
	if !got["emilia.tail-scale.ts.net"]["2.2.2.0/24"] {
		t.Errorf("emilia.tail-scale.ts.net missing 2.2.2.0/24")
	}
}

// TestIndexNodesApprovedRoutes_PointersNoMutation pins the
// "shared set across aliases" guarantee: the same Set pointer
// sits under both aliases (not two copies). This is a subtle
// invariant — if we accidentally copy the map, we double the
// memory and risk future drift between the aliases.
func TestIndexNodesApprovedRoutes_PointersNoMutation(t *testing.T) {
	nodes := []headscale.NodeView{
		{
			Hostname:      "node",
			GivenName:      "node.tail-scale.ts.net",
			ApprovedRoutes: []string{"104.16.0.0/12"},
		},
	}
	got := indexNodesApprovedRoutes(nodes)
	if !reflect.DeepEqual(got["node"], got["node.tail-scale.ts.net"]) {
		t.Errorf("BOTH aliases should reference the same set; got %v vs %v",
			got["node"], got["node.tail-scale.ts.net"])
	}
	// Pointer identity: the underlying map is the SAME map (not a copy).
	// We can't compare reflectively, but we can prove it via mutation:
	got["node"]["new-test-key"] = true
	if !got["node.tail-scale.ts.net"]["new-test-key"] {
		t.Errorf("mutation via one alias did not propagate; the two aliases are different maps")
	}
}
