// B402 (2026-10-20) — a heartbeat that arrives must beat a state that says it did not.
//
// THE MEASUREMENT (operator's installation, the same day):
//
//	cluster_node: svyatoslava | roles={skygate-standby} | state=failed
//	              skygate_version=v1.5.108 | joined 2026-10-09 09:45
//	              last_seen_at = 2026-10-20 07:45   ← the CURRENT minute
//	cluster_audit: 2026-10-09 17:40 node_health {"to":"failed","from":"ready",
//	              "actor":"elector","reason":"last_seen 17m43s ago (3+ …"}
//
// The stanza read `failed` because the HA elector said so when heartbeats stopped, and
// NOTHING but the rolling-upgrade orchestrator could ever reverse that — `RejoinNode`
// had exactly one caller. So a node that kept heartbeating every 30 s stayed "dead"
// for eleven days, and the only way out was hand-written SQL. `skygate_version` was
// equally frozen: the heartbeat carried node_id + token only, so the version written at
// JOIN time was the last one the row would ever see (v1.5.108 against a v1.5.142 hub).
package cluster

import (
	"database/sql"
	"testing"
	"time"

	"skygate/internal/db"
)

// b402OpenDB migrates a throwaway SQLite database through the real chain and seeds the
// cluster + one standby row in the given state.
func b402OpenDB(t *testing.T, state, version string) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b402.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging','staging','[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state,
		                          skygate_version, joined_at, last_seen_at)
		VALUES ('node-disc-svyatoslava','skygate-staging','svyatoslava','100.64.0.9',
		        '{skygate-standby}', $1, $2, $3, $3)`,
		state, version, db.ActiveDialect().TimeValue(time.Now().UTC())); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return d
}

// b402Heartbeat performs one authenticated heartbeat for the seeded node: a real
// invite token (the verifier requires sgn1.<payload>.<sig>) bound to that node id.
func b402Heartbeat(t *testing.T, d *sql.DB, version string) *HeartbeatResponse {
	t.Helper()
	inviteID, token, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, "secret")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	if _, err := d.Exec(`UPDATE cluster_invite SET used_by_node_id = $1 WHERE id = $2`,
		"node-disc-svyatoslava", inviteID); err != nil {
		t.Fatalf("bind invite: %v", err)
	}
	resp, err := Heartbeat(d, "secret", &HeartbeatRequest{
		NodeID: "node-disc-svyatoslava", Token: token, Version: version,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	return resp
}

// TestB402_HeartbeatClearsFailed is the core contract: an authenticated heartbeat
// reverses the elector's `failed` verdict, because the verdict's premise ("no heartbeat
// for 3 intervals") is no longer true.
func TestB402_HeartbeatClearsFailed(t *testing.T) {
	d := b402OpenDB(t, NodeStateFailed, "v1.5.108+59fbad7")
	resp := b402Heartbeat(t, d, "v1.5.144+1847ff75")
	if resp.State != NodeStateReady {
		t.Fatalf("state after heartbeat = %q, want ready — the failed verdict no longer holds", resp.State)
	}
	var state, version string
	if err := d.QueryRow(`SELECT state, skygate_version FROM cluster_node WHERE id='node-disc-svyatoslava'`).
		Scan(&state, &version); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != NodeStateReady || version != "v1.5.144+1847ff75" {
		t.Fatalf("row = (state=%q version=%q), want ready + the build that just heartbeated", state, version)
	}
}

// TestB402_DrainingIsNotClearedByAHeartbeat — the operator's own decision must survive
// the network: a draining node is being removed on purpose.
func TestB402_DrainingIsNotClearedByAHeartbeat(t *testing.T) {
	d := b402OpenDB(t, NodeStateDraining, "v1.5.144")
	b402Heartbeat(t, d, "")
	var state string
	if err := d.QueryRow(`SELECT state FROM cluster_node WHERE id='node-disc-svyatoslava'`).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != NodeStateDraining {
		t.Fatalf("state = %q, want draining — a heartbeat must not undo a drain", state)
	}
}

// TestB402_OlderAgentDoesNotEraseTheVersion — a heartbeat without a version (an agent
// built before B402) leaves the stored build alone instead of blanking it.
func TestB402_OlderAgentDoesNotEraseTheVersion(t *testing.T) {
	d := b402OpenDB(t, NodeStateReady, "v1.5.108+59fbad7")
	b402Heartbeat(t, d, "")
	var version string
	if err := d.QueryRow(`SELECT skygate_version FROM cluster_node WHERE id='node-disc-svyatoslava'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != "v1.5.108+59fbad7" {
		t.Fatalf("version = %q, want the stored one untouched", version)
	}
}

// TestB402_TheReverseTransitionIsAudited — the resurrection must be explainable: the
// audit trail carries the elector's verdict, the heartbeat, and the build.
func TestB402_TheReverseTransitionIsAudited(t *testing.T) {
	d := b402OpenDB(t, NodeStateFailed, "v1.5.108")
	b402Heartbeat(t, d, "v1.5.144")
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM cluster_audit WHERE action='node_health' AND target_node_id='node-disc-svyatoslava'`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n == 0 {
		t.Fatal("failed → ready left no node_health audit row — the reversal would be invisible")
	}
}
