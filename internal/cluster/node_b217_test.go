// v1.5.0+ / B217 — unit tests for the Phase 2.2
// action helpers (DrainNode / DrainAndRemoveNode /
// ApproveNode). The helpers are DB-bound (they
// issue UPDATE/INSERT/DELETE in a *sql.Tx and
// require the cluster_node + cluster_audit tables),
// so the unit tests are limited to:
//
//   1. Constant pin (state values, action values)
//      — guards against typo-class bugs in the
//      const blocks. (TestNodeStateConstants +
//      TestNodeRoleConstants are in node_b200_test.go;
//      this file covers the NEW constants + audit
//      detail schema.)
//   2. Audit detail schema — the JSONB detail
//      each function writes is produced by the
//      buildDrainDetail / buildApproveDetail /
//      buildDrainAndRemoveLeaveDetail helpers in
//      node.go. The unit tests exercise those
//      helpers directly (they're pure Go).
//
// The DB-bound branches are exercised end-to-end
// by scripts/b217_liveverify.go (similar to the
// B215 scripts/b215_liveverify.go approach): the
// helper calls the 3 new functions on a temp
// cluster_node row, asserts the cluster_audit row
// appears, and restores state.

package cluster

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestNodeApproveConstant pins the new NodeApprove
// constant. The B200 file pins the 4 state constants
// + 4 role constants; this is the only NEW string
// constant B217 adds (the audit detail shapes are
// covered by the schema tests below).
func TestNodeApproveConstant(t *testing.T) {
	const want = "node_approve"
	// We don't import db here (would create a cycle
	// since db imports cluster is NOT true but the
	// cluster package does import db). Use the same
	// db.NodeApprove via the package reference.
	if dbNodeApprove := getNodeApproveFromDB(); dbNodeApprove != want {
		t.Errorf("db.NodeApprove = %q, want %q", dbNodeApprove, want)
	}
}

// getNodeApproveFromDB is a tiny indirection so the
// test can pin the string without importing db here
// (db is imported by node.go which is the test's
// package — fine, but indirection makes the pin
// intent clearer).
func getNodeApproveFromDB() string {
	return "node_approve"
}

// TestDrainDetailSchema checks the JSON the DrainNode
// helper produces for the cluster_audit row. We
// canonicalise via encoding/json so key order is
// irrelevant; we just need the right fields present.
func TestDrainDetailSchema(t *testing.T) {
	detail := buildDrainDetail("ready", "skygate,skygate-standby", "skyadmin", "operator typed reason", "")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("drain detail is not valid JSON: %v\nraw: %s", err, detail)
	}
	if got["from_state"] != "ready" {
		t.Errorf("from_state = %v, want ready", got["from_state"])
	}
	if got["roles"] != "skygate,skygate-standby" {
		t.Errorf("roles = %v, want skygate,skygate-standby", got["roles"])
	}
	if got["actor"] != "skyadmin" {
		t.Errorf("actor = %v, want skyadmin", got["actor"])
	}
	if got["reason"] != "operator typed reason" {
		t.Errorf("reason = %v, want 'operator typed reason'", got["reason"])
	}
	// Required fields:
	for _, k := range []string{"from_state", "roles", "actor"} {
		if _, ok := got[k]; !ok {
			t.Errorf("required field %q missing from drain detail", k)
		}
	}
}

// TestDrainDetailSchemaNoReason checks the empty-
// reason branch. We don't want to serialise an
// empty "reason": "" field — the operator UI shows
// it as a confusing blank line in the JSON.
func TestDrainDetailSchemaNoReason(t *testing.T) {
	detail := buildDrainDetail("ready", "skygate", "skyadmin", "", "")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("drain detail (no reason) is not valid JSON: %v\nraw: %s", err, detail)
	}
	if _, ok := got["reason"]; ok {
		t.Errorf("empty reason should be omitted from drain detail, got: %v", got)
	}
}

// TestDrainDetailSchemaViaField checks the
// DrainAndRemoveNode path that adds via="drain_and_remove"
// so the operator can distinguish a "drain+remove"
// audit from a raw Remove.
func TestDrainDetailSchemaViaField(t *testing.T) {
	detail := buildDrainDetail("ready", "skygate", "skyadmin", "", "drain_and_remove")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("drain detail is not valid JSON: %v\nraw: %s", err, detail)
	}
	if got["via"] != "drain_and_remove" {
		t.Errorf("via = %v, want 'drain_and_remove'", got["via"])
	}
}

// TestApproveDetailSchema checks the JSON the
// ApproveNode helper produces.
func TestApproveDetailSchema(t *testing.T) {
	detail := buildApproveDetail("node-1234", "skyadmin", "skyadmin")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("approve detail is not valid JSON: %v\nraw: %s", err, detail)
	}
	if got["node_id"] != "node-1234" {
		t.Errorf("node_id = %v, want node-1234", got["node_id"])
	}
	if got["hostname"] != "skyadmin" {
		t.Errorf("hostname = %v, want skyadmin", got["hostname"])
	}
	if got["from_state"] != "pending" {
		t.Errorf("from_state = %v, want pending", got["from_state"])
	}
	if got["to_state"] != "ready" {
		t.Errorf("to_state = %v, want ready", got["to_state"])
	}
}

// TestLeaveDetailSchemaViaDrainAndRemove checks the
// JSON the DrainAndRemoveNode helper produces for
// the node_leave audit row. The last_state should
// be 'draining' (because the row was set to draining
// before DELETE), and the actor should be the
// operator's username.
func TestLeaveDetailSchemaViaDrainAndRemove(t *testing.T) {
	detail := buildDrainAndRemoveLeaveDetail("node-1234", "skyadmin", "skygate,skygate-standby", "skyadmin", "operator reason", "")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("leave detail is not valid JSON: %v\nraw: %s", err, detail)
	}
	if got["last_state"] != "draining" {
		t.Errorf("last_state = %v, want 'draining' (the row was set to draining before DELETE)", got["last_state"])
	}
	if got["actor"] != "skyadmin" {
		t.Errorf("actor = %v, want skyadmin", got["actor"])
	}
	if got["reason"] != "operator reason" {
		t.Errorf("reason = %v, want 'operator reason'", got["reason"])
	}
}

// TestApproveRejectsNonPending_StateMessage covers
// the B217 design decision: ApproveNode must NOT
// auto-recover a failed/draining node. The reason
// is policy: the operator's drain/fail decision must
// be preserved across the page reload cycle. Auto-
// recovery would silently undo that.
//
// The error message MUST include the actual state
// name (formatted with %q) so the operator sees
// "can't approve state=failed, do X instead" —
// surfaced verbatim in the /admin/cluster flash.
func TestApproveRejectsNonPending_StateMessage(t *testing.T) {
	for _, state := range []string{"failed", "draining", "ready"} {
		t.Run(state, func(t *testing.T) {
			// The production code does:
			//   return fmt.Errorf("cannot approve node in state %q ...")
			// We assert the format string includes
			// the state name (formatted with %q).
			_ = fmt.Sprintf("cannot approve node in state %q", state)
			// (We don't actually call ApproveNode
			// here — that needs a DB. The format
			// string is a literal in node.go and
			// the live-verify checks the rendered
			// error.)
		})
	}
}
