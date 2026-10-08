// B365 — gap 3: a JOIN must adopt the cluster_node row that already exists for
// its hostname.
//
// Live on the reference standby (2026-10-08) the row read
//
//	id=node-disc-svyatoslava  hostname=svyatoslava  state=ready
//	skygate_version=(discovered via Tailscale)
//
// after the join AND after Approve: the B223 discovery pass had created the row
// with "(discovered via Tailscale)" and `ON CONFLICT (id) DO NOTHING`, and the
// join's own upsert never landed. These tests drive the REAL Join entry point
// against a migrated SQLite database (the native install's backend) so the
// property — "the row for the joining hostname ends up state=pending with the
// joining build's version" — is pinned behaviourally, not by grepping SQL.
package cluster

import (
	"testing"
	"time"

	"skygate/internal/db"
)

const b365Secret = "b365-test-secret"

// TestB365_JoinAdoptsDiscoveredRow is the regression for the live row: the
// discovery pass runs FIRST, then a real join for the same hostname.
func TestB365_JoinAdoptsDiscoveredRow(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, DefaultClusterID); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	// The B223 discovery pass created the row the operator saw.
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "svyatoslava", "100.64.0.20", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode: %v", err)
	}
	before, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("LookupNode before join: %v", err)
	}
	if before.ID != "node-disc-svyatoslava" {
		t.Fatalf("fixture: discovery row id = %q, want node-disc-svyatoslava", before.ID)
	}

	_, tok, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, b365Secret)
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	resp, err := Join(d, b365Secret, &JoinRequest{
		Token: tok, Hostname: "svyatoslava", TailscaleIP: "100.64.0.20",
		SkygateVersion: "v1.5.107+c0ffee1", Roles: NodeRoleStandby,
	})
	if err != nil {
		t.Fatalf("Join: %v (the join of a discovered host must succeed)", err)
	}

	after, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("LookupNode after join: %v", err)
	}
	// THE PROPERTY. State is what the Approve button needs; the version is the
	// operator's evidence that the host that joined is the build the invite
	// named, instead of the discovery placeholder.
	if after.State != NodeStatePending {
		t.Errorf("state after join = %q, want %q (Approve only accepts pending)", after.State, NodeStatePending)
	}
	if after.SkygateVer != "v1.5.107+c0ffee1" {
		t.Errorf("skygate_version after join = %q, want the joining build %q — the row kept the discovery label",
			after.SkygateVer, "v1.5.107+c0ffee1")
	}
	// Exactly ONE row for the hostname, and the id the discovery pass used is
	// preserved: the invite's used_by_node_id and any already-running heartbeat
	// daemon name it.
	if resp.NodeID != before.ID {
		t.Errorf("join returned node_id %q, want the existing row's id %q (a heartbeat daemon may already be using it)",
			resp.NodeID, before.ID)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_node WHERE cluster_id = $1 AND hostname = $2`,
		DefaultClusterID, "svyatoslava").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("cluster_node holds %d rows for the hostname, want exactly 1", rows)
	}
	// The invite must be bound to the SAME id the heartbeat path will present.
	inv, err := LookupInvite(d, inviteIDOf(t, tok))
	if err != nil {
		t.Fatalf("LookupInvite: %v", err)
	}
	if inv.UsedByNodeID != before.ID {
		t.Errorf("invite used_by_node_id = %q, want %q", inv.UsedByNodeID, before.ID)
	}
	// The adopt path must be visible in the audit trail too — the live host had
	// no node_join event at all.
	var joins int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_join'`).Scan(&joins); err != nil {
		t.Fatalf("count node_join audits: %v", err)
	}
	if joins == 0 {
		t.Error("no node_join audit row — a join that adopted a discovered row left no trace")
	}
}

// TestB365_JoinAdoptsPanelRow covers the other existing-row shape: the panel's
// own onboard action creates the row with an empty skygate_version before the
// host runs the block.
func TestB365_JoinAdoptsPanelRow(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, DefaultClusterID); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	if _, err := AddNode(d, DefaultClusterID, "svyatoslava", "", []string{NodeRoleStandby}, ""); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	_, tok, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, b365Secret)
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	if _, err := Join(d, b365Secret, &JoinRequest{
		Token: tok, Hostname: "svyatoslava", TailscaleIP: "100.64.0.20",
		SkygateVersion: "v1.5.107+c0ffee1", Roles: NodeRoleStandby,
	}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	after, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("LookupNode: %v", err)
	}
	if after.State != NodeStatePending {
		t.Errorf("state = %q, want pending", after.State)
	}
	if after.SkygateVer != "v1.5.107+c0ffee1" {
		t.Errorf("skygate_version = %q, want the joining build", after.SkygateVer)
	}
	if len(after.Roles) != 1 || after.Roles[0] != NodeRoleStandby {
		t.Errorf("roles = %v, want [%s]", after.Roles, NodeRoleStandby)
	}
}

// TestB365_JoinAfterApproveResetsToPending pins the reason the adopt path forces
// state=pending: a second join must put the row back behind the operator's
// Approve gate, and the version must follow the joining build (a standby
// upgraded and re-joined).
func TestB365_JoinAfterApproveResetsToPending(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, DefaultClusterID); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	_, tok1, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, b365Secret)
	if err != nil {
		t.Fatalf("IssueInvite 1: %v", err)
	}
	if _, err := Join(d, b365Secret, &JoinRequest{
		Token: tok1, Hostname: "svyatoslava", TailscaleIP: "100.64.0.20",
		SkygateVersion: "v1.5.106", Roles: NodeRoleStandby,
	}); err != nil {
		t.Fatalf("Join 1: %v", err)
	}
	if err := ApproveNode(d, DefaultClusterID, "svyatoslava", "operator"); err != nil {
		t.Fatalf("ApproveNode: %v", err)
	}
	// A heartbeat would set ready; that is the steady state we re-join from.
	if _, err := d.Exec(`UPDATE cluster_node SET state = 'ready' WHERE hostname = $1`, "svyatoslava"); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	_, tok2, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, b365Secret)
	if err != nil {
		t.Fatalf("IssueInvite 2: %v", err)
	}
	if _, err := Join(d, b365Secret, &JoinRequest{
		Token: tok2, Hostname: "svyatoslava", TailscaleIP: "100.64.0.21",
		SkygateVersion: "v1.5.107+c0ffee1", Roles: NodeRoleStandby,
	}); err != nil {
		t.Fatalf("Join 2: %v", err)
	}
	after, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("LookupNode: %v", err)
	}
	if after.State != NodeStatePending {
		t.Errorf("state after re-join = %q, want pending (the operator's Approve gate)", after.State)
	}
	if after.SkygateVer != "v1.5.107+c0ffee1" {
		t.Errorf("skygate_version after re-join = %q, want the new build", after.SkygateVer)
	}
	if after.TailscaleIP != "100.64.0.21" {
		t.Errorf("tailscale_ip = %q, want the re-join's address", after.TailscaleIP)
	}
}

// inviteIDOf extracts the cluster_invite id from a freshly minted token.
func inviteIDOf(t *testing.T, tok string) string {
	t.Helper()
	p, err := VerifyToken(b365Secret, tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	return p.Inv
}

// TestB365_JoinInsertConflictsOnTheHostnameKey pins the SQL-level half of the
// fix. The pre-B365 statement read `ON CONFLICT (id)`, but `id` is NOT the key a
// row created by the discovery pass collides on — `cluster_node` carries TWO
// unique keys (the `id` PRIMARY KEY and the `(cluster_id, hostname)` unique
// index), replayed here by a single INSERT that conflicts with BOTH at once:
//
//	(1) the adopted row's id      -> node-disc-svyatoslava
//	(2) its (cluster_id, hostname) -> (skygate-staging, svyatoslava)
//
// `ON CONFLICT (id)` cannot absorb that; the join failed with a unique
// constraint violation and the operator saw the discovery row instead. The
// statement below is the one Join now issues, including the `RETURNING id` the
// caller scans into its node id — so the join ends up bound to the row the
// discovery pass already created, with the joining build's version on it,
// rather than aborting or leaving the discovery label in place.
func TestB365_JoinInsertConflictsOnTheHostnameKey(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, DefaultClusterID); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "svyatoslava", "100.64.0.20", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode: %v", err)
	}

	dialect := db.ActiveDialect()
	now := time.Now().UTC()
	// The competing id is derived from an invite, i.e. NOT the discovery row's
	// id — exactly the shape a join produces.
	gotID := "node-000000000000"
	err := d.QueryRow(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at, last_seen_at
		) VALUES ($1, $2, $3, $4, `+dialect.CastTextArray("$5")+`, 'pending', $6, $7, $7)
		ON CONFLICT (cluster_id, hostname) DO UPDATE SET
			tailscale_ip = EXCLUDED.tailscale_ip,
			roles = EXCLUDED.roles,
			skygate_version = EXCLUDED.skygate_version,
			state = 'pending',
			joined_at = EXCLUDED.joined_at,
			last_seen_at = EXCLUDED.last_seen_at
		RETURNING id
	`, gotID, DefaultClusterID, "svyatoslava", "100.64.0.20",
		db.TextArrayLiteral([]string{NodeRoleStandby}), "v1.5.107+c0ffee1",
		dialect.TimeValue(now)).Scan(&gotID)
	if err != nil {
		t.Fatalf("the join INSERT must absorb a (cluster_id, hostname) conflict: %v", err)
	}
	if gotID != "node-disc-svyatoslava" {
		t.Errorf("RETURNING id = %q, want the conflicting row's id node-disc-svyatoslava (the id must not be rewritten)", gotID)
	}
	row, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("LookupNode: %v", err)
	}
	if row.SkygateVer != "v1.5.107+c0ffee1" {
		t.Errorf("skygate_version = %q, want the joining build", row.SkygateVer)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_node WHERE hostname = $1`, "svyatoslava").Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows for the hostname, want 1 — the ON CONFLICT target must be the unique key that fires", rows)
	}
}
