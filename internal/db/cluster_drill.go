// Package db — cluster_drill.go owns the drill-mode
// counterpart of cluster_failover.go. Phase 3.6 of
// docs/internal/cluster-management.md.
//
// Background
//
// The operator wanted a "safe" failover to verify the
// B204 elector + B205 failover + Phase 3.4 manual
// failover all work end-to-end without committing to
// a real swap. A drill is the same atomic swap as a
// real failover (target promoted to skygate, old
// primary demoted to state=draining) but writes a
// `node_drill` row to cluster_audit instead of
// `node_failover` — so:
//
//   1. The /admin/ha "Last 20 events" table shows the
//      drill alongside real failovers (the operator
//      can see both).
//   2. The /admin/audit UNION query (B207) and the
//      db.GetClusterAuditByAction filter can show
//      ONLY failovers (filter action IN ('node_health',
//      'failover_recommend', 'node_failover')) or
//      ONLY drills (filter action = 'node_drill').
//   3. The operator can see at a glance which failovers
//      were test runs vs production swaps.
//
// The drill is genuinely a swap (the cluster_node state
// changes). To "undo" a drill, the operator runs
// `skygate cluster failover --target=<old_primary>` —
// the same command they would use for a normal swap.
// There's no separate "rollback" command because the
// underlying state machine is symmetric.
//
// The /admin/ha page's "Last 20 events" table includes
// node_drill rows in the "Last 20 HA events" filter
// (B208.2 extended the WHERE clause to include
// node_drill alongside node_failover + node_health +
// failover_recommend).

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// DrillClusterNode is the drill-mode counterpart of
// FailoverClusterNode — same atomic swap, same eligibility
// rules, same ErrNoPrimary / ErrNotEligibleForFailover
// sentinels, but writes action='node_drill' to
// cluster_audit (instead of 'node_failover').
//
// The function is intentionally a near-copy of
// FailoverClusterNode rather than a wrapper: the drill
// audit row is operator-facing (the operator sees
// "drill" in the action column, immediately greps for
// "drill" in audit_log when debugging, etc.) so the
// two helpers stay clearly separate in the code. If
// FailoverClusterNode's eligibility logic ever changes
// (e.g. the "must have role=skygate-standby" check
// gets a new condition), DrillClusterNode's copy must
// be updated in lock-step — the test suite (B212 +
// future B-checks) pins both via the B-check contracts.
//
// The from/to IDs + audit row detail follow the same
// shape as FailoverClusterNode. The detail JSONB
// includes an extra "drill": true flag so future
// audit-log filters can distinguish drill-and-real
// events even if the action column is missing.
func DrillClusterNode(d *sql.DB, targetID, actor, reason string) (fromID, toID string, err error) {
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		return "", "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

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
		return "", "", fmt.Errorf("target is already the current primary")
	}
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

	if _, err = tx.Exec(`
		UPDATE cluster_node
		SET roles = ARRAY(
			SELECT DISTINCT unnest(roles || ARRAY['skygate']::text[])
		)
		WHERE id = $1
	`, targetID); err != nil {
		return "", "", fmt.Errorf("promote target: %w", err)
	}
	if _, err = tx.Exec(`
		UPDATE cluster_node
		SET state = 'draining',
		    roles = array_remove(roles, 'skygate')
		WHERE id = $1
	`, fromIDRow); err != nil {
		return "", "", fmt.Errorf("demote old primary: %w", err)
	}
	// Drill audit row: same shape as FailoverClusterNode's
	// node_failover row, with two extra fields:
	//   "drill": true        — explicit marker for the
	//                        "this was a test swap" semantic
	//   "real_action": "node_failover"
	//                      — what the row WOULD have been if
	//                        this were a real failover (so
	//                        audit-log filters written for
	//                        FailoverClusterNode can re-tag
	//                        the row as a drill if needed)
	if _, err = tx.Exec(`
		INSERT INTO cluster_audit (
			cluster_id, action, target_node_id, actor, detail
		) VALUES (
			'skygate-staging', 'node_drill', $1, $2, $3
		)
	`, targetID, actor, fmt.Sprintf(
		`{"from_id":"%s","from":"%s","to_id":"%s","to":"%s","actor":"%s","reason":"%s","drill":true,"real_action":"node_failover"}`,
		fromIDRow, fromHostRow, targetID, toHostRow, actor, reason,
	)); err != nil {
		return "", "", fmt.Errorf("insert cluster_audit: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit: %w", err)
	}
	return fromIDRow, targetID, nil
}
