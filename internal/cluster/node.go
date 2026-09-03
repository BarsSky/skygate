// Package cluster — node.go owns the cluster_node CRUD
// helpers. Phase 2.2 of docs/internal/cluster-management.md.
//
// The cluster_node table (B195 schema) tracks every node
// in the cluster: hostname, tailscale IP, roles, state,
// when it joined, when it was last seen. Phase 2.2 adds
// admin-driven "add" and "remove" — the join bootstrap
// (which writes cluster_node from the new node's side
// when it consumes an invite) lands in Phase 4.

package cluster

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"skygate/internal/db"
)

// NodeState values. The DB column is TEXT with a default
// of "pending" so we keep these as string constants rather
// than a typed enum (a real enum would force a migration
// for every new state, and the admin UI already deals
// with strings).
const (
	NodeStatePending  = "pending"  // row created by /admin/cluster add, no heartbeat yet
	NodeStateReady    = "ready"    // heartbeat observed by the agent
	NodeStateDraining = "draining" // admin marked for removal
	NodeStateFailed   = "failed"   // last heartbeat too old; auto-promote candidate
)

// NodeRole values are free-form strings (cluster_node.roles
// is TEXT[]) but the common ones are pinned here so the
// admin UI can offer them as a select.
const (
	NodeRoleSkygate        = "skygate"         // primary skygate control plane
	NodeRoleStandby        = "skygate-standby" // standby skygate
	NodeRolePatroniPrimary = "patroni-primary" // PG primary (might be on a non-skygate host)
	NodeRolePatroniReplica = "patroni-replica" // PG replica
)

// ErrNodeNotFound is returned by RemoveNode / LookupNode
// when the row doesn't exist.
var ErrNodeNotFound = errors.New("cluster node not found")

// Node is the in-memory shape of one cluster_node row.
// Roles is []string because the DB column is TEXT[].
type Node struct {
	ID            string
	ClusterID     string
	Hostname      string
	TailscaleIP   string
	Roles         []string
	State         string
	SkygateVer    string
	JoinedAt      time.Time
	LastSeenAt    time.Time
}

// LookupNode returns the cluster_node row with the given
// hostname (within the given cluster). Hostname is the
// natural key (operator's mental model = "the node named
// X"), not the row id.
func LookupNode(d *sql.DB, clusterID, hostname string) (*Node, error) {
	if clusterID == "" || hostname == "" {
		return nil, ErrNodeNotFound
	}
	row := d.QueryRow(`
		SELECT id, cluster_id, hostname, COALESCE(tailscale_ip, ''),
		       roles, state, COALESCE(skygate_version, ''),
		       joined_at, last_seen_at
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
	`, clusterID, hostname)
	return scanNode(row)
}

// AddNode inserts a new cluster_node row in "pending" state
// (no heartbeat yet). Returns the new row's id. Duplicate
// hostnames within the same cluster return an error
// (the admin UI checks first, but the DB is the ultimate
// guard via the host_id UNIQUE INDEX we'll add in B195.1
// — for now we just rely on the admin-side pre-check + a
// UNIQUE constraint we'll add in a follow-up migration).
//
// Auto-creates the cluster row if it doesn't exist yet
// (FK constraint on cluster_node.cluster_id means the
// parent row must exist before the child INSERT can
// succeed). Idempotent.
func AddNode(d *sql.DB, clusterID, hostname, tailscaleIP string, roles []string, skygateVer string) (string, error) {
	if clusterID == "" {
		return "", errors.New("cluster: empty cluster_id")
	}
	if hostname == "" {
		return "", errors.New("cluster: empty hostname")
	}
	if len(roles) == 0 {
		roles = []string{NodeRoleSkygate}
	}
	// Auto-create the parent cluster row if missing.
	if err := EnsureCluster(d, clusterID, clusterID); err != nil {
		return "", fmt.Errorf("ensure cluster: %w", err)
	}
	now := time.Now().UTC()
	var id string
	err := d.QueryRow(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at, last_seen_at
		) VALUES (
			'node-' || substr(md5(random()::text), 1, 12),
			$1, $2, $3, $4, 'pending',
			$5, $6, $6
		)
		RETURNING id
	`, clusterID, hostname, tailscaleIP, pqStringArray(roles), skygateVer, now).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// RemoveNode deletes the cluster_node row matching
// (cluster_id, hostname). Idempotent: removing a non-
// existent row is a no-op (returns nil, not an error).
//
// B215: also emits a cluster_audit row with action
// 'node_leave' (best-effort). The audit row is written
// BEFORE the DELETE so the row's target_node_id still
// exists in cluster_node (cluster_audit.target_node_id
// has no FK, but a non-NULL target_node_id is easier
// to JOIN against the live row than a NULL one). The
// detail captures the row's last state + roles for
// post-mortem analysis.
//
// B217: prefer DrainAndRemoveNode (which sets
// state=draining first and writes a node_drain audit
// row before the node_leave audit + DELETE). This
// function is the "force delete" path — useful when
// the operator needs to clean up a stuck node in
// state=draining that won't leave, but the standard
// flow for an active node is drain-then-remove.
func RemoveNode(d *sql.DB, clusterID, hostname string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	// B215: snapshot the row + emit the audit event
	// before deleting. We do this in a single
	// transaction so a failed DELETE doesn't leave a
	// phantom audit row.
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var nodeID, lastState, rolesText string
	if scanErr := tx.QueryRow(`
		SELECT id, COALESCE(state, ''), COALESCE(array_to_string(roles, ','), '')
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
	`, clusterID, hostname).Scan(&nodeID, &lastState, &rolesText); scanErr != nil {
		// Row doesn't exist — nothing to delete or
		// audit. The downstream DELETE will also be
		// a no-op. Idempotent.
		_ = tx.Rollback()
		return nil
	}
	// Best-effort audit. If the audit INSERT fails,
	// we still proceed with the DELETE (the operator
	// clicked Remove; failing the whole op would be
	// worse than a missing audit row).
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeLeave, nodeID, hostname,
		fmt.Sprintf(`{"node_id":%q,"hostname":%q,"last_state":%q,"roles":%q}`,
			nodeID, hostname, lastState, rolesText)); auditErr != nil {
		fmt.Fprintf(os.Stderr, "cluster.RemoveNode: warning: node_leave audit failed: %v (continuing with delete)\n", auditErr)
	}
	if _, err = tx.Exec(`
		DELETE FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
	`, clusterID, hostname); err != nil {
		return fmt.Errorf("delete cluster_node: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// DrainNode sets state=draining on a cluster_node row
// without deleting it. The HA chain sees state=draining
// and the operator can still inspect the row (the
// "frozen state" view) before issuing a separate Remove.
//
// B217 (v1.5.0+): this is the Phase 2.2 "drain" step
// — the operator presses a "Drain" button, the node
// stops accepting new work (state=draining in the HA
// chain), and a separate "Drain & Remove" button can
// later delete the row.
//
// The function emits a node_drain cluster_audit row
// in the same transaction as the UPDATE so a failed
// UPDATE rolls back the audit. detail captures the
// row's previous state (typically "ready" or "failed")
// for the post-mortem timeline.
//
// Idempotent: draining a node already in state=draining
// is a no-op (returns nil, no audit row). This matters
// because the /admin/cluster page may show the same
// node in multiple browser tabs — the operator might
// click Drain twice. The second click should be silent.
//
// `actor` is recorded in the cluster_audit row's
// actor column (the admin username, or "system" for
// automated drains). `reason` is free-text the
// operator typed in the /admin/cluster form; stored
// in the audit detail.
func DrainNode(d *sql.DB, clusterID, hostname, actor, reason string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	if actor == "" {
		actor = "system"
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var nodeID, prevState, rolesText string
	if scanErr := tx.QueryRow(`
		SELECT id, COALESCE(state, ''), COALESCE(array_to_string(roles, ','), '')
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
		 FOR UPDATE
	`, clusterID, hostname).Scan(&nodeID, &prevState, &rolesText); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ErrNodeNotFound
		}
		return fmt.Errorf("lookup node: %w", scanErr)
	}
	// Idempotent: already draining → no-op.
	if prevState == NodeStateDraining {
		_ = tx.Rollback()
		return nil
	}
	if _, execErr := tx.Exec(`
		UPDATE cluster_node SET state = $1
		 WHERE id = $2
	`, NodeStateDraining, nodeID); execErr != nil {
		return fmt.Errorf("update state: %w", execErr)
	}
	// Audit. detail.reason is optional — if the
	// operator didn't type anything, we leave it out
	// of the JSON rather than serialise an empty
	// field.
	detailJSON := buildDrainDetail(prevState, rolesText, actor, reason, "")
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeDrain, nodeID, actor, detailJSON); auditErr != nil {
		return fmt.Errorf("insert node_drain audit: %w", auditErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// DrainAndRemoveNode is the Phase 2.2 "drain + leave +
// cleanup" combo: sets state=draining first (so the
// HA chain sees the node go offline), then deletes
// the row. Both audit rows (node_drain + node_leave)
// are written in the same transaction so a failed
// DELETE rolls back BOTH audits (and the state
// update).
//
// Use this for the standard operator-driven remove
// flow. Use RemoveNode (raw DELETE + node_leave) only
// when the operator needs to clean up a stuck node
// that's already in a weird state.
//
// `actor` + `reason` flow through to both audit rows
// (the node_drain detail.reason and the node_leave
// detail.reason are the same — the operator's intent
// was "this node is going away because X").
func DrainAndRemoveNode(d *sql.DB, clusterID, hostname, actor, reason string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	if actor == "" {
		actor = "system"
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var nodeID, prevState, rolesText string
	if scanErr := tx.QueryRow(`
		SELECT id, COALESCE(state, ''), COALESCE(array_to_string(roles, ','), '')
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
		 FOR UPDATE
	`, clusterID, hostname).Scan(&nodeID, &prevState, &rolesText); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ErrNodeNotFound
		}
		return fmt.Errorf("lookup node: %w", scanErr)
	}
	// Step 1: set state=draining. If the node was
	// already draining (rare — the operator pressed
	// the button twice), the UPDATE is a no-op and we
	// still emit the audit row to mark the "leave"
	// event with a fresh timestamp.
	if prevState != NodeStateDraining {
		if _, execErr := tx.Exec(`
			UPDATE cluster_node SET state = $1
			 WHERE id = $2
		`, NodeStateDraining, nodeID); execErr != nil {
			return fmt.Errorf("update state to draining: %w", execErr)
		}
	}
	// Step 2: node_drain audit (idempotent: the
	// pre-existing state=draining row from a prior
	// DrainNode call will also have its own audit,
	// so we'll get 2 audits per drain+remove — that's
	// the intended trace: one for "marked as draining",
	// one for "actually left").
	drainDetail := buildDrainDetail(prevState, rolesText, actor, reason, "drain_and_remove")
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeDrain, nodeID, actor, drainDetail); auditErr != nil {
		return fmt.Errorf("insert node_drain audit: %w", auditErr)
	}
	// Step 3: DELETE the row.
	if _, execErr := tx.Exec(`
		DELETE FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
	`, clusterID, hostname); execErr != nil {
		return fmt.Errorf("delete cluster_node: %w", execErr)
	}
	// Step 4: node_leave audit.
	leaveDetail := buildDrainAndRemoveLeaveDetail(nodeID, hostname, rolesText, actor, reason, "")
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeLeave, nodeID, actor, leaveDetail); auditErr != nil {
		return fmt.Errorf("insert node_leave audit: %w", auditErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ApproveNode transitions a cluster_node row from
// state=pending to state=ready. Phase 2.2 (B217) — the
// operator presses an "Approve" button on /admin/cluster
// after seeing a new node appear in state=pending (the
// /api/cluster/join endpoint sets pending; the first
// successful heartbeat auto-transitions to ready, but
// some deployments want explicit admin approval before
// the node is allowed to participate in the HA chain).
//
// The function emits a node_approve cluster_audit row
// in the same transaction as the UPDATE.
//
// Idempotent: approving a node already in state=ready
// is a no-op (returns nil, no audit row). Approving
// a node in state=draining or state=failed is ALSO a
// no-op (we don't auto-recover failed nodes — the
// operator must explicitly transition them via
// skygate init re-run on the box, or by editing the
// row directly).
//
// `actor` is recorded in the cluster_audit row's
// actor column (the admin username).
func ApproveNode(d *sql.DB, clusterID, hostname, actor string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	if actor == "" {
		actor = "system"
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var nodeID, prevState string
	if scanErr := tx.QueryRow(`
		SELECT id, COALESCE(state, '')
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
		 FOR UPDATE
	`, clusterID, hostname).Scan(&nodeID, &prevState); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ErrNodeNotFound
		}
		return fmt.Errorf("lookup node: %w", scanErr)
	}
	// Idempotent: already ready → no-op.
	if prevState == NodeStateReady {
		_ = tx.Rollback()
		return nil
	}
	// Refuse to approve non-pending nodes (don't
	// auto-recover failed / draining). The operator
	// must explicitly handle those (e.g. by re-running
	// skygate init on the box, or editing the row).
	if prevState != NodeStatePending {
		_ = tx.Rollback()
		return fmt.Errorf("cannot approve node in state %q (only state=pending can be approved; for failed/draining, re-run skygate init on the box)", prevState)
	}
	if _, execErr := tx.Exec(`
		UPDATE cluster_node SET state = $1, last_seen_at = NOW()
		 WHERE id = $2
	`, NodeStateReady, nodeID); execErr != nil {
		return fmt.Errorf("update state: %w", execErr)
	}
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeApprove, nodeID, actor,
		buildApproveDetail(nodeID, hostname, actor)); auditErr != nil {
		return fmt.Errorf("insert node_approve audit: %w", auditErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RejoinNode transitions a node from state=draining or
// state=failed back to state=ready. The B222 rolling
// upgrade orchestrator calls this after the per-node
// upgrade (binary push + restart + healthz poll)
// completes successfully. The pre-B222 behaviour for a
// draining node was "operator must run `skygate init`
// on the box again" — but that's a destructive action
// (it tries to re-register the node in headscale + the
// HA chain), not what the operator wants after a
// planned rolling upgrade. The new RejoinNode is
// NON-destructive: it only flips state + updates
// last_seen_at; it does NOT touch headscale or the HA
// chain. The heartbeat daemon (B201) takes care of
// re-registering the node's heartbeats in the new
// state.
//
// Allowed transitions:
//
//	pending  → rejected (use ApproveNode instead)
//	draining → ready    (the post-upgrade path)
//	failed   → ready    (the post-failover path; manual
//	                     recovery is the same as upgrade
//	                     from the orchestrator's POV)
//	ready    → no-op    (idempotent; no audit row)
//
// `actor` is recorded in the cluster_audit row's actor
// column. For B222 this is typically the orchestrating
// admin's username, since RejoinNode is called from the
// /admin/cluster/upgrade handler. A future B-block
// could expose RejoinNode as a "Mark Ready" button on
// /admin/cluster so the operator can manually recover
// a failed node without going through the upgrade flow
// — but B222 scopes it to the orchestrator path.
func RejoinNode(d *sql.DB, clusterID, hostname, actor string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	if actor == "" {
		actor = "system"
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var nodeID, prevState string
	if scanErr := tx.QueryRow(`
		SELECT id, COALESCE(state, '')
		  FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
		 FOR UPDATE
	`, clusterID, hostname).Scan(&nodeID, &prevState); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ErrNodeNotFound
		}
		return fmt.Errorf("lookup node: %w", scanErr)
	}
	// Idempotent: already ready → no-op.
	if prevState == NodeStateReady {
		_ = tx.Rollback()
		return nil
	}
	// Refuse to rejoin pending nodes — that's
	// ApproveNode's job (it adds the initial
	// last_seen_at + writes a node_approve audit
	// row, which the B215 /admin/ha filter
	// distinguishes from node_rejoin).
	if prevState == NodeStatePending {
		_ = tx.Rollback()
		return fmt.Errorf("cannot rejoin node in state %q (use ApproveNode for pending nodes; RejoinNode is for draining/failed)", prevState)
	}
	if _, execErr := tx.Exec(`
		UPDATE cluster_node SET state = $1, last_seen_at = NOW()
		 WHERE id = $2
	`, NodeStateReady, nodeID); execErr != nil {
		return fmt.Errorf("update state: %w", execErr)
	}
	if _, auditErr := db.InsertClusterAudit(tx, clusterID, db.NodeRejoin, nodeID, actor,
		buildRejoinDetail(nodeID, hostname, prevState, actor)); auditErr != nil {
		return fmt.Errorf("insert node_rejoin audit: %w", auditErr)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// UpsertNode inserts-or-updates the cluster_node row for
// (clusterID, hostname). Used by the B211 `skygate init`
// bootstrap path: on first run it creates the row in
// 'ready' state with the given roles; on re-run it
// refreshes tailscale_ip / skygate_version / roles to
// the new values WITHOUT resetting the state (so a node
// in 'draining' or 'failed' is NOT silently flipped back
// to 'ready' — the operator's drain/failover decision
// must be preserved).
//
// Returns the row's id (existing or newly created).
//
// Auto-creates the parent cluster row if missing (same
// FK-contract dance as AddNode / IssueInvite).
//
// `roles` is required; pass at least one role. The
// canonical "this node is the primary" set is
// [NodeRoleSkygate]; "this node is a standby" is
// [NodeRoleStandby].
//
// `skygateVer` is informational only — it's the
// SKYGATE_VERSION env var on the box, so an operator
// looking at /admin/cluster can see "node X is on
// skygate v1.5.0+ (commit abc1234)".
func UpsertNode(d *sql.DB, clusterID, hostname, tailscaleIP string, roles []string, skygateVer string) (string, error) {
	if clusterID == "" {
		return "", errors.New("cluster: empty cluster_id")
	}
	if hostname == "" {
		return "", errors.New("cluster: empty hostname")
	}
	if len(roles) == 0 {
		return "", errors.New("cluster: empty roles — at least NodeRoleSkygate / NodeRoleStandby is required")
	}
	// Auto-create the parent cluster row if missing
	// (FK constraint — same as AddNode / IssueInvite).
	if err := EnsureCluster(d, clusterID, clusterID); err != nil {
		return "", fmt.Errorf("ensure cluster: %w", err)
	}
	now := time.Now().UTC()
	// ON CONFLICT (cluster_id, hostname) DO UPDATE:
	//   - refresh tailscale_ip / skygate_version
	//   - replace roles with the new set
	//   - DO NOT touch state / joined_at / last_seen_at
	//     (preserves operator's drain/failover decisions
	//     and the join timestamp)
	//   - last_seen_at = now() so the watchdog + elector
	//     see "this node is alive" right after init.
	var id string
	err := d.QueryRow(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at, last_seen_at
		) VALUES (
			'node-' || substr(md5(random()::text), 1, 12),
			$1, $2, $3, $4, 'ready',
			$5, $6, $6
		)
		ON CONFLICT (cluster_id, hostname) DO UPDATE SET
			tailscale_ip = EXCLUDED.tailscale_ip,
			roles = EXCLUDED.roles,
			skygate_version = EXCLUDED.skygate_version,
			last_seen_at = EXCLUDED.last_seen_at
		RETURNING id
	`, clusterID, hostname, tailscaleIP, pqStringArray(roles), skygateVer, now).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// scanNode is the shared scanner for both LookupNode and
// any future ListNodes helper. Accepts either *sql.Row or
// *sql.Rows (via the Scanner interface) so the same code
// path handles single-row + multi-row reads.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanNode(r rowScanner) (*Node, error) {
	var n Node
	var rolesStr string
	var tailscaleIP, skygateVer string
	var joinedAt, lastSeenAt sql.NullTime
	if err := r.Scan(
		&n.ID, &n.ClusterID, &n.Hostname, &tailscaleIP,
		&rolesStr, &n.State, &skygateVer,
		&joinedAt, &lastSeenAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	n.TailscaleIP = tailscaleIP
	n.SkygateVer = skygateVer
	if joinedAt.Valid {
		n.JoinedAt = joinedAt.Time
	}
	if lastSeenAt.Valid {
		n.LastSeenAt = lastSeenAt.Time
	}
	n.Roles = parsePGTextArray(rolesStr)
	return &n, nil
}

// pqStringArray formats a Go []string as a postgres
// TEXT[] literal of the form "{a,b,c}". pgx accepts
// this form on input even though it returns []string
// on output, so the round-trip is symmetric.
func pqStringArray(roles []string) string {
	if len(roles) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, r := range roles {
		if i > 0 {
			b.WriteByte(',')
		}
		// quote if the role contains a comma, quote, or
		// backslash (rare for our short role list, but
		// let's be safe)
		if strings.ContainsAny(r, `,"\`) {
			b.WriteByte('"')
			for _, c := range r {
				if c == '"' || c == '\\' {
					b.WriteByte('\\')
				}
				b.WriteRune(c)
			}
			b.WriteByte('"')
		} else {
			b.WriteString(r)
		}
	}
	b.WriteByte('}')
	return b.String()
}

// parsePGTextArray parses a postgres TEXT[] literal of
// the form "{a,b,c}" into a []string. Empty / NULL
// returns nil. Defined as a package-local (not exported)
// because the admin package has its own copy; we keep
// both in case the role formatting diverges in the future.
//
// (For Phase 2.2 this is duplicated; if we end up with
// 3+ call sites we should move to a shared helper in
//  internal/db/.)
func parsePGTextArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" || s == "NULL" {
		return nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return []string{s}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		out = append(out, p)
	}
	return out
}

// ---------- Pure detail builders (B217) ----------------------
//
// Extracted from the DrainNode / DrainAndRemoveNode /
// ApproveNode body so the JSON shape can be unit-tested
// without a DB (see node_b217_test.go). The production
// code uses these helpers directly; the tests pin the
// schema. If you change one, change both.

func buildDrainDetail(fromState, roles, actor, reason, via string) string {
	out := fmt.Sprintf(`{"from_state":%q,"roles":%q,"actor":%q`, fromState, roles, actor)
	if reason != "" {
		out += fmt.Sprintf(`,"reason":%q`, reason)
	}
	if via != "" {
		out += fmt.Sprintf(`,"via":%q`, via)
	}
	out += "}"
	return out
}

func buildApproveDetail(nodeID, hostname, actor string) string {
	return fmt.Sprintf(`{"node_id":%q,"hostname":%q,"from_state":"pending","to_state":"ready","actor":%q}`,
		nodeID, hostname, actor)
}

// buildRejoinDetail — the audit row for RejoinNode
// (B222). Mirrors buildApproveDetail's shape but with
// the from_state=draining|failed dynamic (ApproveNode
// is always from_state=pending). The to_state is
// always "ready". The B222 orchestrator always passes
// a draining or failed node; the test for "what
// state was the node in" is the audit's from_state
// field.
func buildRejoinDetail(nodeID, hostname, fromState, actor string) string {
	return fmt.Sprintf(`{"node_id":%q,"hostname":%q,"from_state":%q,"to_state":"ready","actor":%q}`,
		nodeID, hostname, fromState, actor)
}

func buildDrainAndRemoveLeaveDetail(nodeID, hostname, roles, actor, reason, _ string) string {
	out := fmt.Sprintf(`{"node_id":%q,"hostname":%q,"last_state":"draining","roles":%q,"actor":%q`, nodeID, hostname, roles, actor)
	if reason != "" {
		out += fmt.Sprintf(`,"reason":%q`, reason)
	}
	out += "}"
	return out
}
