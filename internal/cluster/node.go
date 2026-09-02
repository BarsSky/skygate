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
	"strings"
	"time"
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
func RemoveNode(d *sql.DB, clusterID, hostname string) error {
	if clusterID == "" || hostname == "" {
		return ErrNodeNotFound
	}
	_, err := d.Exec(`
		DELETE FROM cluster_node
		 WHERE cluster_id = $1 AND hostname = $2
	`, clusterID, hostname)
	return err
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
