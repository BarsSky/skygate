package admin

// devices_test.go — regression tests for the v0.30.1
// user-device-can't-be-exit-node guard (nodeTagRefusedForUserDevice).
//
// The bug: on 2026-07-28, user1's Windows box "base"
// (headscale id=7, tag:dev-user1-base) was found carrying
// tag:exit-node in headscale. The Tailscale client on base
// auto-selected "Base" as exit-node (0ms self-loop = lowest
// metric), and all internet traffic from base went to /dev/null.
// User reported: "пропал доступ в сеть" + "exit node не
// выбирается корректно".
//
// Root cause: tag:exit-node was set via direct headscale CLI
// outside of skygate (no audit_log entry for node=7), so
// skygate never had a chance to refuse the request. The
// v0.30.1 fix adds a guard in PostAdminNodeTag that refuses
// the same shape of request when it comes through the skygate
// admin UI (the most common accidental path: admin clicks
// "Tag as exit-node" on the wrong row in /admin/devices).
//
// refactor-v0.30 Phase B step 3a: tests moved here from
// internal/handlers/handlers_admin_nodes_test.go (the
// function-under-test moved with them).

import (
	"strings"
	"testing"
)

// TestNodeTagRefused_ExitNodeOnUserDevice — the primary
// regression test. A node with tag:dev-user1-base (per-user
// device) must refuse tag:exit-node.
func TestNodeTagRefused_ExitNodeOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base", "tag:private"}
	refused, msg, hadTag := nodeTagRefusedForUserDevice(7, "tag:exit-node", current)
	if !refused {
		t.Fatalf("expected refuse for tag:exit-node on per-user device, got refused=false msg=%q", msg)
	}
	if !strings.Contains(msg, "tag:dev-user1-base") {
		t.Errorf("refuse message should mention existing dev tag, got: %s", msg)
	}
	if !strings.Contains(msg, "tag:exit-node") {
		t.Errorf("refuse message should mention attempted tag, got: %s", msg)
	}
	if hadTag != "tag:dev-user1-base" {
		t.Errorf("existingDevTag should be the dev tag, got %q", hadTag)
	}
}

// TestNodeTagRefused_PerRelayExitTag — also refused. tag:exit-relay-1
// (per-relay name) is also tag:exit-* and would make the user
// device an exit-node candidate.
func TestNodeTagRefused_PerRelayExitTag(t *testing.T) {
	current := []string{"tag:dev-admin-workstation-1", "tag:private"}
	refused, msg, _ := nodeTagRefusedForUserDevice(9, "tag:exit-relay-1", current)
	if !refused {
		t.Fatalf("expected refuse for tag:exit-relay-1 on per-user device, got refused=false msg=%q", msg)
	}
	if !strings.Contains(msg, "Per-user devices") {
		t.Errorf("refuse message should explain the rule, got: %s", msg)
	}
}

// TestNodeTagAllowed_ExitNodeOnRelay — POSITIVE case. A relay
// (no per-user tag) MUST be allowed to get tag:exit-node.
// This is the legitimate "Tag as exit-node" flow on /admin/exit-nodes.
func TestNodeTagAllowed_ExitNodeOnRelay(t *testing.T) {
	current := []string{"tag:public"} // relay-1-style: no dev tag
	refused, msg, hadTag := nodeTagRefusedForUserDevice(3, "tag:exit-node", current)
	if refused {
		t.Fatalf("tag:exit-node on a relay (no dev tag) must be allowed, got refused=true msg=%q hadTag=%q", msg, hadTag)
	}
	if hadTag != "" {
		t.Errorf("hadTag should be empty when allowed, got %q", hadTag)
	}
}

// TestNodeTagAllowed_PrivateOnUserDevice — POSITIVE case.
// tag:private on a per-user device is fine (it's the normal
// "auto-apply tag:private" path in backfillNodeOwnership).
// The guard must NOT over-fire on tag:private.
func TestNodeTagAllowed_PrivateOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base"}
	refused, msg, _ := nodeTagRefusedForUserDevice(7, "tag:private", current)
	if refused {
		t.Fatalf("tag:private on a per-user device must be allowed (it's the normal flow), got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_PublicOnUserDevice — POSITIVE case.
// tag:public is not an exit-node tag, must be allowed even
// on a per-user device (catches a regression where the prefix
// check was too greedy).
func TestNodeTagAllowed_PublicOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base"}
	refused, msg, _ := nodeTagRefusedForUserDevice(7, "tag:public", current)
	if refused {
		t.Fatalf("tag:public on a per-user device must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_SubnetRouterOnUserDevice — POSITIVE case.
// tag:subnet-router is a role tag, not an exit-node. Allowed.
func TestNodeTagAllowed_SubnetRouterOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-admin-workstation-1"}
	refused, msg, _ := nodeTagRefusedForUserDevice(9, "tag:subnet-router", current)
	if refused {
		t.Fatalf("tag:subnet-router on a per-user device must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_ExitNodeOnEmptyNode — POSITIVE case.
// A node with NO tags yet is a "fresh" node. Tagging it
// tag:exit-node is the normal "promote a fresh VPS to relay"
// flow (this is exactly what /admin/exit-nodes does).
func TestNodeTagAllowed_ExitNodeOnEmptyNode(t *testing.T) {
	refused, msg, _ := nodeTagRefusedForUserDevice(99, "tag:exit-node", nil)
	if refused {
		t.Fatalf("tag:exit-node on a fresh (tag-less) node must be allowed, got refused=true msg=%q", msg)
	}
	refused, msg, _ = nodeTagRefusedForUserDevice(99, "tag:exit-node", []string{})
	if refused {
		t.Fatalf("tag:exit-node on a fresh (empty-tag) node must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagRefused_ExitNodeOnMultipleDevTags — a node with
// multiple tag:dev-* (e.g. a misconfigured edge case) — the
// guard fires on the FIRST one it finds, but the message
// reports which one it hit (so the operator can untag it).
func TestNodeTagRefused_ExitNodeOnMultipleDevTags(t *testing.T) {
	current := []string{
		"tag:dev-admin-workstation-1",
		"tag:dev-admin-workstation-2",
		"tag:private",
	}
	refused, msg, hadTag := nodeTagRefusedForUserDevice(9, "tag:exit-relay-3", current)
	if !refused {
		t.Fatalf("expected refuse, got allowed (msg=%q)", msg)
	}
	// We don't pin WHICH dev tag the guard reports — it
	// iterates in order. But it must be one of them.
	if !strings.HasPrefix(hadTag, "tag:dev-admin-") {
		t.Errorf("hadTag should be a admin dev tag, got %q", hadTag)
	}
}
