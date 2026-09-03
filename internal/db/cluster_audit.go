// Package db — cluster_audit.go owns the cluster_audit
// INSERT helper. Phase 2.6 of
// docs/internal/cluster-management.md (B215):
// "Bootstrap state machine: init / join / drain / leave
// events".
//
// Before B215, only the cluster failover path
// (B204/B205/Phase 3.4/3.6) wrote to cluster_audit —
// the bootstrap paths (init, join, drain, leave)
// were silent. Operators had no audit trail for
// "who bootstrapped this node, when did the last
// standby join, when did we drain the old primary".
//
// The helper below is the central entry point for
// writing cluster_audit rows. Pre-B215, the failover
// path had its own inline INSERT (cluster_failover.go
// line 195); the new code paths use this helper so
// the JSONB shape is consistent across actions.
//
// The cluster_audit table is shared with /admin/ha
// (its "Last 20 HA events" query filters on
// `action IN ('node_health', 'failover_recommend',
// 'node_failover', 'node_drill')`) and /admin/audit
// (its UNION ALL of audit_log + cluster_audit). B215
// adds 'node_init', 'node_join', 'node_drain',
// 'node_leave' to that filter list.

package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// auditExec is the common interface between *sql.DB and
// *sql.Tx. We accept both so the B215 callers (which
// need the INSERT to be transactional with the
// surrounding DELETE in RemoveNode) don't have to
// manage two helper variants.
//
// B215 contract: pass the SAME exec surface the
// surrounding work uses. If you're in a transaction,
// pass the *sql.Tx. If you're in autocommit (the
// common case), pass the *sql.DB.
type auditExec interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// ClusterAuditAction is a typed enum for the cluster_audit
// action column. Using typed constants (rather than raw
// strings) prevents typo-class bugs at call sites. The
// string values match the in-DB column exactly so a
// future JSONB action filter matches.
type ClusterAuditAction string

// B215: bootstrap state machine events. The first
// three (init / join / leave) are new in B215; the
// rest (node_health / failover_recommend /
// node_failover / node_drill) were already in use
// from B204 / B205 / Phase 3.4 / Phase 3.6.
const (
	// NodeInit — fired by `skygate init` (B211) when
	// the operator bootstraps a node as the cluster
	// primary. Detail: {"node_id":..., "roles":[...],
	// "skygate_version":..., "tailscale_ip":...}.
	NodeInit ClusterAuditAction = "node_init"

	// NodeJoin — fired by `skygate join` (B212) when
	// a new node registers via /api/cluster/join.
	// Idempotent: a re-join of an existing node also
	// fires this event (the ON CONFLICT path refreshes
	// tailscale_ip + skygate_version). Detail:
	// {"node_id":..., "tailscale_ip":..., "version":...,
	// "roles":[...]}.
	NodeJoin ClusterAuditAction = "node_join"

	// NodeDrain — fired when a node's state is set
	// to 'draining'. Today this happens in two paths:
	//   (a) FailoverClusterNode demotes the old
	//       primary (state=draining) as part of
	//       node_failover.
	//   (b) (Future) an admin clicks a "Drain" button
	//       on /admin/cluster (Phase 2.2.x follow-up).
	// Detail: {"node_id":..., "from_state":"ready",
	// "reason":"..."}.
	NodeDrain ClusterAuditAction = "node_drain"

	// NodeLeave — fired by cluster.RemoveNode when
	// the operator deletes a cluster_node row (via
	// the "Remove" button on /admin/cluster).
	// Detail: {"node_id":..., "hostname":...,
	// "last_state":"ready|draining|failed", "actor":...}.
	NodeLeave ClusterAuditAction = "node_leave"

	// NodeHealth — fired by the B204 elector when a
	// node's state changes (pending→ready→failed, etc).
	NodeHealth ClusterAuditAction = "node_health"

	// FailoverRecommend — fired by the B204 elector
	// when it detects a failed primary + a ready
	// standby candidate.
	FailoverRecommend ClusterAuditAction = "failover_recommend"

	// NodeFailover — fired by db.FailoverClusterNode
	// (Phase 3.4 admin button + B205 CLI) when a real
	// failover completes.
	NodeFailover ClusterAuditAction = "node_failover"

	// NodeDrill — fired by db.DrillClusterNode (Phase
	// 3.6) when a failover-drill completes. The drill
	// does the same atomic swap as a real failover
	// but writes node_drill to cluster_audit (not
	// node_failover) so the /admin/ha "Last 20 events"
	// table can tell them apart.
	NodeDrill ClusterAuditAction = "node_drill"

	// NodeApprove — fired by cluster.ApproveNode (B217,
	// Phase 2.2) when the operator presses the
	// "Approve" button on /admin/cluster for a
	// state=pending node. The pre-B217 path auto-
	// transitioned pending→ready on the first
	// heartbeat (cluster.Heartbeat still does this);
	// the new explicit-approval flow adds a manual
	// gate so some deployments can require admin
	// sign-off before a new standby joins the HA
	// chain. Detail: {"node_id":..., "from_state":
	// "pending", "to_state": "ready", "actor": "..."}.
	NodeApprove ClusterAuditAction = "node_approve"

	// NodeRejoin — fired by cluster.RejoinNode (B222,
	// Phase 4.2) when the rolling-upgrade orchestrator
	// marks a node state=ready after the upgrade
	// completed (the transition is draining→ready or
	// failed→ready). The ApproveNode action covers
	// the pending→ready transition; RejoinNode is the
	// post-upgrade version that "this node WAS marked
	// for removal (draining) or had a transient
	// failure (failed), and is now back in service".
	// Detail: {"node_id":..., "from_state": "draining"
	// | "failed", "to_state": "ready", "actor": "..."}.
	// Idempotent: ready→ready is a no-op (no audit
	// row written — the B204 elector would otherwise
	// see duplicate events on every orchestrator
	// restart).
	NodeRejoin ClusterAuditAction = "node_rejoin"

	// NodeDiscovered — fired by cluster.EnsureDiscoveredNode
	// (B223, Phase 4.3) when the Tailscale
	// auto-discovery poller finds a new peer on the
	// tailnet and inserts a cluster_node row in
	// state=pending. This is the audit row the
	// operator greps for "where did this pending
	// node come from?" — the answer is "the B223
	// poller auto-created it from the Tailscale
	// peer list". Detail: {"node_id": "node-disc-<h>",
	// "hostname": "...", "tailscale_ip": "...",
	// "discovered_at": "<rfc3339>"}. The
	// /admin/cluster page renders a "Discovered via
	// Tailscale" badge next to rows with this
	// action in their history (the B215 /admin/ha
	// filter surfaces this for nodes that
	// transitioned from discovered → approved).
	NodeDiscovered ClusterAuditAction = "node_discovered"
)

// InsertClusterAudit inserts one row into cluster_audit.
// This is the canonical entry point for the bootstrap
// state machine events (B215). The pre-B215 inline
// INSERT in cluster_failover.go is still there (it
// participates in the failover transaction; moving it
// out would break the atomicity contract).
//
// Arguments:
//   - exec: an *sql.DB (autocommit) or *sql.Tx (part of
//     a larger transaction). Use the same surface as
//     the surrounding work — passing a Tx lets the
//     caller rollback the audit + the operation
//     together; passing a DB makes the audit autocommit
//     (best-effort).
//   - clusterID: the cluster the event belongs to.
//     Currently always "skygate-staging" (the
//     single-cluster deployment).
//   - action: the typed event (NodeInit / NodeJoin /
//     NodeDrain / NodeLeave / etc).
//   - targetNodeID: the cluster_node.id the event is
//     about. May be empty for events that aren't
//     tied to a specific node (currently none — every
//     B215 event has a target).
//   - actor: who/what triggered the event. For
//     operator-driven events (init/join/leave via
//     CLI), this is the operator's username or
//     hostname. For auto-driven events (drain via
//     failover), this is "system" or the actor passed
//     to FailoverClusterNode.
//   - detail: free-form JSON object as a string
//     (e.g. `{"from_state":"ready","reason":"..."}`).
//     The caller is responsible for JSON validity —
//     we don't parse/re-marshal. The empty string is
//     stored as '{}' for the SQL NOT NULL constraint.
//
// Returns: the new row's id (positive int64) on
// success; error otherwise. Errors are NOT swallowed
// (the caller decides whether to log + continue or
// return).
func InsertClusterAudit(exec auditExec, clusterID string, action ClusterAuditAction, targetNodeID, actor, detail string) (int64, error) {
	// Normalize the detail. Empty string → '{}' (the
	// column is NOT NULL with default '{}' in the
	// schema, but a literal empty would still pass
	// that NOT NULL check — we normalize for
	// downstream JSONB consumers that might choke
	// on "").
	detailNorm := strings.TrimSpace(detail)
	if detailNorm == "" {
		detailNorm = "{}"
	}
	targetNorm := strings.TrimSpace(targetNodeID)
	actorNorm := strings.TrimSpace(actor)
	var id int64
	err := exec.QueryRow(`
		INSERT INTO cluster_audit (
			cluster_id, action, target_node_id, actor, detail
		) VALUES (
			$1, $2, NULLIF($3, ''), $4, $5::jsonb
		)
		RETURNING id
	`, clusterID, string(action), targetNorm, actorNorm, detailNorm).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert cluster_audit (action=%s): %w", string(action), err)
	}
	return id, nil
}
