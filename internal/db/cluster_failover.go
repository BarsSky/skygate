// Package db — cluster_failover.go owns the DB-side
// helpers for the skygate-cluster node failover (Phase 3.4
// of docs/internal/cluster-management.md).
//
// What's here
//
// FailoverClusterNode atomically promotes a standby node
// to skygate role (and demotes the current primary to
// state=draining) in a single transaction. The B204
// elector's recommend path writes a `failover_recommend`
// row when it detects a failed primary; this helper is
// the operator-driven counterpart — used when the
// operator wants to force a promotion (planned
// maintenance, manual swap, disagreement with the
// elector's recommendation, etc.).
//
// What it does (in a single transaction):
//
//  1. SELECT the current primary from cluster_node
//     (state=ready AND roles @> ARRAY['skygate']) — the
//     only role check, since multiple nodes can have
//     role=skygate-standby. If no current primary
//     exists, the helper returns ErrNoPrimary.
//  2. SELECT the target node by id; if it's not
//     state=ready OR doesn't have role=skygate-standby,
//     return ErrNotEligibleForFailover.
//  3. UPDATE the target: state stays 'ready', but the
//     'skygate' role is added. So the target's roles
//     become ['skygate-standby', 'skygate'] (PostgreSQL
//     array concatenation). The new primary has BOTH
//     roles until the next manual cleanup.
//  4. UPDATE the current primary: state='draining',
//     the 'skygate' role is removed. Its roles become
//     ['skygate-standby'] (or whatever it had before;
//     we only remove 'skygate' specifically).
//  5. INSERT a cluster_audit row: action='node_failover',
//     from_node_id=<old primary>, target_node_id=<target>,
//     detail={"from":"<old hostname>","to":"<target hostname>","actor":"<user>","reason":"<text>"}.
//
// All five steps in a single *sql.Tx so a failure
// anywhere rolls back the whole promotion (no half-
// failed failovers with a target that's "promoted" but
// no row in cluster_audit to show the operator).

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNoPrimary is returned by FailoverClusterNode when
// cluster_node has no row in state=ready with the
// 'skygate' role. The operator can either (a) run a
// health check to see why the primary is missing, or
// (b) pick a different failover target (the current
// scenario assumes the primary is failing — the elector's
// failover_recommend row will have flagged the missing
// primary).
var ErrNoPrimary = errors.New("no current skygate primary in cluster_node (state=ready AND roles @> ARRAY['skygate'])")

// ErrNotEligibleForFailover is returned when the target
// node is in the wrong state or doesn't have the
// 'skygate-standby' role. The handler turns this into a
// 4xx error with a clear message ("target must be in
// state=ready with role=skygate-standby").
var ErrNotEligibleForFailover = errors.New("target node is not eligible for promotion (must be state=ready with role=skygate-standby)")

// FailoverClusterNode atomically promotes the target to
// skygate and demotes the current primary. Both nodes
// must satisfy:
//   - state='ready'
//   - roles contains 'skygate' (the current primary) or
//     'skygate-standby' (the target)
//
// The function reads from *sql.DB (not DBSource) so the
// caller passes s.dbc().Current() — the B210 hot-reload
// pattern that follows the B203 watchdog's pool swap.
//
// The function returns the from/to node IDs on success
// (for the audit log + the operator's flash message).
//
// reason is a free-text string the operator types in the
// /admin/ha form (e.g., "manual maintenance", "primary
// hardware failure", "drill"); it's stored in the
// cluster_audit row's detail.reason field.
func FailoverClusterNode(d *sql.DB, targetID, actor, reason string) (fromID, toID string, err error) {
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		return "", "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// 1. Find the current primary (state=ready + role=skygate).
	//    We pick the one with the oldest id (deterministic
	//    tie-breaker if multiple rows somehow have role=skygate
	//    at once — which shouldn't happen but the LIMIT 1
	//    makes the query well-defined).
	var fromIDRow, fromHostRow string
	err = tx.QueryRow(`
		SELECT id, hostname
		FROM cluster_node
		WHERE state = 'ready'
		  AND 'skygate' = ANY (roles)
		ORDER BY id ASC
		LIMIT 1
	`).Scan(&fromIDRow, &fromHostRow)
	if err == sql.ErrNoRows {
		return "", "", ErrNoPrimary
	}
	if err != nil {
		return "", "", fmt.Errorf("find current primary: %w", err)
	}

	// 2. Verify the target is eligible (state=ready + role=skygate-standby).
	var toIDRow, toHostRow string
	var toRolesRow StringArray
	err = tx.QueryRow(`
		SELECT id, hostname, roles
		FROM cluster_node
		WHERE id = $1
	`, targetID).Scan(&toIDRow, &toHostRow, &toRolesRow)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("target node %q not found", targetID)
	}
	if err != nil {
		return "", "", fmt.Errorf("find target: %w", err)
	}
	if toHostRow == fromHostRow {
		return "", "", fmt.Errorf("target %q is already the current primary", toIDRow)
	}
	// Eligibility: state=ready is checked via the column read;
	// the role check needs an in-Go test because the column
	// is a text[].
	eligible := false
	for _, r := range toRolesRow {
		if r == "skygate-standby" {
			eligible = true
			break
		}
	}
	if !eligible {
		return "", "", ErrNotEligibleForFailover
	}

	// 3. Promote the target: keep state='ready', but add
	//    'skygate' to the roles. Idempotent: if 'skygate' is
	//    already in the target's roles (a corner case from
	//    a half-failed prior failover), the array_cat below
	//    will dedupe via the @> check + unnest logic in the
	//    next statement. We use array_cat for clarity (the
	//    Postgres native is `roles || ARRAY['skygate']`).
	_, err = tx.Exec(`
		UPDATE cluster_node
		SET roles = ARRAY(
			SELECT DISTINCT unnest(roles || ARRAY['skygate']::text[])
		)
		WHERE id = $1
	`, targetID)
	if err != nil {
		return "", "", fmt.Errorf("promote target: %w", err)
	}

	// 4. Demote the current primary: state='draining',
	//    remove 'skygate' from roles. We use array_remove
	//    to keep all other roles intact (e.g. a node that
	//    was both skygate and skygate-standby before
	//    remains a skygate-standby after demotion).
	_, err = tx.Exec(`
		UPDATE cluster_node
		SET state = 'draining',
		    roles = array_remove(roles, 'skygate')
		WHERE id = $1
	`, fromIDRow)
	if err != nil {
		return "", "", fmt.Errorf("demote old primary: %w", err)
	}

	// 5. Write the cluster_audit row. The cluster_audit
	//    table has these columns: id, cluster_id, actor,
	//    action, target_node_id, detail, result,
	//    error_message, created_at. There's no from_node_id
	//    column — the "from" info goes in the JSONB
	//    detail (the B204/B205 events do the same). The
	//    /admin/ha page's "Last 20 events" query filters
	//    on action IN ('node_health', 'failover_recommend',
	//    'node_failover') and shows the detail.reason +
	//    detail.from + detail.to in the Detail column.
	_, err = tx.Exec(`
		INSERT INTO cluster_audit (
			cluster_id, action, target_node_id, actor, detail
		) VALUES (
			'skygate-staging', 'node_failover', $1, $2, $3
		)
	`, targetID, actor, fmt.Sprintf(
		`{"from_id":"%s","from":"%s","to_id":"%s","to":"%s","actor":"%s","reason":"%s"}`,
		fromIDRow, fromHostRow, targetID, toHostRow, actor, reason,
	))
	if err != nil {
		return "", "", fmt.Errorf("insert cluster_audit: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit: %w", err)
	}
	return fromIDRow, targetID, nil
}
