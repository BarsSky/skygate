// Package cluster — join.go owns the join flow: a new
// node uses an sgn1 invite token to register itself
// in the cluster, and then sends periodic heartbeats
// to keep its cluster_node row fresh.
//
// v1.5.0+ / B201 — Phase 2.3 of
// docs/internal/cluster-management.md.
//
// Two functions:
//
//   - Join — POST /api/cluster/join
//     The new node calls this once at startup with the
//     sgn1 token + its own hostname + tailscale_ip +
//     skygate_version. The server verifies the token,
//     looks up the cluster_invite row, checks it's
//     still pending, creates the cluster_node row,
//     marks the invite as used, and returns the
//     new node's id (used for subsequent heartbeats).
//
//   - Heartbeat — POST /api/cluster/heartbeat
//     The new node calls this every ~30s with its
//     node_id + the original token. The server
//     verifies the token, checks the node_id matches
//     the invite's used_by_node_id, updates
//     last_seen_at, and auto-transitions state from
//     "pending" to "ready" on the first successful
//     heartbeat.
//
// The token serves as both authentication (the new
// node doesn't have a skygate session cookie) and
// authorization (the token's payload names the
// target_hostname — a node can only join with the
// hostname the invite was generated for).
//
// (The token is reused for every heartbeat. A future
// improvement — B202 — would generate a long-lived
// "node token" at join time and return it, so the
// original sgn1 doesn't sit in /var/log forever. For
// now the operational risk is low: a stolen token
// can only spam heartbeats, which the server
// tolerates.)

package cluster

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
)

// ErrNodeAlreadyExists is returned by Join when the
// hostname is already in cluster_node. The new node
// should be idempotent: if it crashed mid-join and is
// retrying, the cluster_node row is already there. The
// API surface treats this as a non-fatal conflict (the
// caller can extract the existing node_id from the
// wrapped error if it wants to keep going).
var ErrNodeAlreadyExists = errors.New("cluster node already exists")

// ErrInviteExpired is returned by Join when the invite's
// expires_at is in the past. (revoked invites return
// ErrInviteAlreadyUsed; expired is a separate case
// because the server should not log an alert when an
// invite naturally times out.)
var ErrInviteExpired = errors.New("cluster invite expired")

// ErrInviteRevoked is returned by Join when the invite
// has been explicitly revoked via /admin/cluster/invite/revoke.
var ErrInviteRevoked = errors.New("cluster invite revoked")

// ErrInviteNotPending is a generic "the invite exists
// but is not in pending state" catch-all. The specific
// sentinel (ErrInviteAlreadyUsed / ErrInviteRevoked) is
// preferred where it applies.
var ErrInviteNotPending = errors.New("cluster invite not pending")

// ErrHostnameMismatch is returned by Join when the
// request's hostname doesn't match the token's target
// hostname. The token is bound to a specific host —
// a new node that was invited as "svi-1" can't claim
// to be "evil-host".
var ErrHostnameMismatch = errors.New("hostname does not match token target")

// JoinRequest is the JSON body the new node POSTs to
// /api/cluster/join.
type JoinRequest struct {
	Token          string `json:"token"`
	Hostname       string `json:"hostname"`
	TailscaleIP    string `json:"tailscale_ip"`
	SkygateVersion string `json:"skygate_version"`
	Roles          string `json:"roles"` // comma-sep, optional; defaults to "skygate-standby"
}

// JoinResponse is what /api/cluster/join returns on
// success. The new node stores NodeID for the
// heartbeat path; the rest is bootstrap info the new
// node uses to configure itself.
type JoinResponse struct {
	ClusterID     string `json:"cluster_id"`
	NodeID        string `json:"node_id"`
	Hostname      string `json:"hostname"`
	DSNTemplate   string `json:"dsn_template"`   // raw template (the %s is unsubstituted) — kept for backward compat with the B200 / B201 clients that want to do their own substitution
	DSN           string `json:"dsn"`            // B212: the template with %s substituted by the primary's reachable hostname (e.g. Tailscale hostname). Empty if no primary is configured (the standby falls back to its own .env DSN).
	PrimaryHost   string `json:"primary_host"`   // B212: the hostname we substituted into DSN. Useful for the standby to log + verify the DSN points where it expects.
	DBName        string `json:"dbname"`
	DBUsername    string `json:"db_username"`
	HeartbeatHint int    `json:"heartbeat_seconds"` // recommended heartbeat interval
}

// Join validates the token, looks up the invite, checks
// the hostname matches the token's target, creates the
// cluster_node row, marks the invite as used, and
// returns the bootstrap info. Returns one of the
// package-level sentinels (ErrInviteAlreadyUsed,
// ErrHostnameMismatch, etc.) on failure so the HTTP
// layer can map them to 4xx codes.
//
// The cluster row is auto-created if missing (FK
// constraint — same as AddNode / IssueInvite).
func Join(d *sql.DB, secret string, req *JoinRequest) (*JoinResponse, error) {
	if secret == "" {
		return nil, errors.New("cluster: empty secret — set SKYGATE_SECRET_KEY")
	}
	if req == nil {
		return nil, errors.New("cluster: nil request")
	}
	if req.Token == "" {
		return nil, errors.New("cluster: empty token")
	}
	if req.Hostname == "" {
		return nil, errors.New("cluster: empty hostname")
	}

	// 1. Verify the token signature + parse the payload.
	payload, err := VerifyToken(secret, req.Token)
	if err != nil {
		return nil, err
	}

	// 2. Auto-create the parent cluster row if missing.
	clusterID := payload.CID
	if clusterID == "" {
		clusterID = DefaultClusterID
	}
	if err := EnsureCluster(d, clusterID, clusterID); err != nil {
		return nil, fmt.Errorf("ensure cluster: %w", err)
	}

	// 3. Look up the invite row, check state.
	invite, err := LookupInvite(d, payload.Inv)
	if err != nil {
		return nil, err
	}
	if invite.Status == "revoked" {
		return nil, ErrInviteRevoked
	}
	if !invite.IsPending(time.Now()) {
		// Either used, expired, or some other state.
		// Differentiate for the HTTP layer.
		if invite.UsedAt != nil {
			return nil, ErrInviteAlreadyUsed
		}
		if invite.ExpiresAt.Before(time.Now()) {
			return nil, ErrInviteExpired
		}
		return nil, ErrInviteNotPending
	}

	// 4. Check the hostname matches the token's target.
	// The token is bound to a specific host — a new node
	// that was invited as "svi-1" can't claim to be
	// "evil-host". Case-insensitive comparison (hostnames
	// are case-insensitive in DNS).
	//
	// B212 fix: an empty TargetHostname means "any
	// host" (a wildcard invite). The pre-B212 code
	// required an exact match even for empty target,
	// which made the `skygate init standby-invite`
	// (B211, which always issues with target="") always
	// fail with ErrHostnameMismatch. Empty target
	// skips the check.
	if invite.TargetHostname != "" && !hostnamesEqual(invite.TargetHostname, req.Hostname) {
		return nil, ErrHostnameMismatch
	}

	// 5. Idempotency: if a cluster_node with this
	// hostname already exists, return the existing one
	// instead of failing. The new node may have crashed
	// mid-join and is retrying.
	if existing, err := LookupNode(d, clusterID, req.Hostname); err == nil && existing != nil {
		// Mark the invite as used (idempotent) and
		// return the existing node_id. This way a retry
		// from the new node is a no-op.
		_, _ = d.Exec(`
			UPDATE cluster_invite
			   SET used_at = COALESCE(used_at, NOW()),
			       used_by_node_id = COALESCE(NULLIF(used_by_node_id, ''), $2)
			 WHERE id = $1 AND used_at IS NULL
		`, payload.Inv, existing.ID)
		// Fetch the dsn_template from cluster_database (if
		// configured) so the new node can bootstrap its
		// own pgxpool. B212 also substitutes the primary's
		// hostname into the template so the standby gets a
		// ready-to-use DSN.
		dsnTpl, dbName, dbUser := readDBBootstrap(d, clusterID)
		primaryHost := readPrimaryHost(d, clusterID)
		return &JoinResponse{
			ClusterID:     clusterID,
			NodeID:        existing.ID,
			Hostname:      existing.Hostname,
			DSNTemplate:   dsnTpl,
			DSN:           substituteDSNTemplate(dsnTpl, primaryHost),
			PrimaryHost:   primaryHost,
			DBName:        dbName,
			DBUsername:    dbUser,
			HeartbeatHint: 30,
		}, nil
	}
	if err != nil && !errors.Is(err, ErrNodeNotFound) {
		return nil, fmt.Errorf("lookup node: %w", err)
	}

	// 6. Parse the roles (comma-sep), default to skygate-standby.
	roles := parseRolesField(req.Roles)
	if len(roles) == 0 {
		roles = []string{NodeRoleStandby}
	}

	// 7. Create the cluster_node row in "pending" state.
	// We use a unique id derived from the invite so a
	// re-join (if the previous node was force-removed)
	// is a clean INSERT, not a flaky UPDATE.
	nodeID := "node-" + payload.Inv[:12]
	now := time.Now().UTC()
	_, err = d.Exec(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at, last_seen_at
		) VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $7)
		ON CONFLICT (id) DO UPDATE SET
			tailscale_ip = EXCLUDED.tailscale_ip,
			skygate_version = EXCLUDED.skygate_version,
			last_seen_at = NOW()
	`, nodeID, clusterID, req.Hostname, req.TailscaleIP,
		pqStringArray(roles), req.SkygateVersion, now)
	if err != nil {
		return nil, fmt.Errorf("insert node: %w", err)
	}

	// 8. Mark the invite as used (atomic with the node
	// INSERT — both inside the same transaction in a
	// future improvement; for now, sequential).
	_, err = d.Exec(`
		UPDATE cluster_invite
		   SET used_at = NOW(),
		       used_by_node_id = $2
		 WHERE id = $1 AND used_at IS NULL
	`, payload.Inv, nodeID)
	if err != nil {
		// Don't fail the join — the node row is already
		// there. A future heartbeat will re-attempt
		// the used_at update.
		_ = err
	}

	// 9. Fetch the bootstrap DSN (if configured) so the
	// new node can point its own pgxpool at the cluster
	// PG. For now, the new node probably doesn't have
	// its own skygate yet — it just runs bootstrap_standby
	// (Phase 7) which uses these values to set up its
	// own .env. B212 also substitutes the primary's
	// hostname into the template so the standby gets a
	// ready-to-use DSN.
	dsnTpl, dbName, dbUser := readDBBootstrap(d, clusterID)
	primaryHost := readPrimaryHost(d, clusterID)

	// 10. B215: emit the node_join audit event. We
	//     use db.InsertClusterAudit (the canonical
	//     helper) so the JSONB shape is consistent
	//     with the failover events. Best-effort: a
	//     failure here doesn't abort the join (the
	//     node row is already committed; the operator
	//     just loses the audit row for this join).
	//     For the same reason, we don't wrap the
	//     insert in the join's transaction (none).
	//     Detail captures the join-relevant fields
	//     so the /admin/ha "Last 20 events" view can
	//     show the new node's metadata.
	roleStr := strings.Join(roles, ",")
	_, _ = db.InsertClusterAudit(d, clusterID, db.NodeJoin, nodeID, req.Hostname,
		fmt.Sprintf(`{"node_id":%q,"hostname":%q,"roles":%q,"tailscale_ip":%q,"skygate_version":%q,"invite_id":%q}`,
			nodeID, req.Hostname, roleStr, req.TailscaleIP, req.SkygateVersion, payload.Inv))

	return &JoinResponse{
		ClusterID:     clusterID,
		NodeID:        nodeID,
		Hostname:      req.Hostname,
		DSNTemplate:   dsnTpl,
		DSN:           substituteDSNTemplate(dsnTpl, primaryHost),
		PrimaryHost:   primaryHost,
		DBName:        dbName,
		DBUsername:    dbUser,
		HeartbeatHint: 30,
	}, nil
}

// HeartbeatRequest is the JSON body the new node POSTs
// to /api/cluster/heartbeat.
type HeartbeatRequest struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"`
}

// HeartbeatResponse is what /api/cluster/heartbeat
// returns on success. State is the new state of the
// node (e.g. "ready" after the first heartbeat).
type HeartbeatResponse struct {
	NodeID                string `json:"node_id"`
	State                 string `json:"state"`
	LastSeenAt            int64  `json:"last_seen_unix"`
	NextHeartbeatSeconds  int    `json:"next_heartbeat_seconds"`
	HeartbeatsUntilStale  int    `json:"heartbeats_until_stale"`
}

// ErrNodeNotFound is returned by Heartbeat when the
// node_id doesn't match any cluster_node row. The
// new node should re-call Join (the server may have
// been restarted, the cluster_node row was force-
// removed, etc).
var ErrHeartbeatNodeNotFound = errors.New("heartbeat: node not found")

// Heartbeat verifies the token, checks the node_id
// matches the invite's used_by_node_id, updates
// last_seen_at, and auto-transitions state from
// "pending" to "ready" on the first successful
// heartbeat. The "failed" transition (3 missed
// heartbeats) is the HA elector's job, not this
// function — Phase 3 territory.
func Heartbeat(d *sql.DB, secret string, req *HeartbeatRequest) (*HeartbeatResponse, error) {
	if secret == "" {
		return nil, errors.New("cluster: empty secret")
	}
	if req == nil {
		return nil, errors.New("cluster: nil request")
	}
	if req.NodeID == "" {
		return nil, errors.New("cluster: empty node_id")
	}
	if req.Token == "" {
		return nil, errors.New("cluster: empty token")
	}

	// 1. Verify the token.
	payload, err := VerifyToken(secret, req.Token)
	if err != nil {
		return nil, err
	}

	// 2. Look up the invite to confirm used_by_node_id.
	invite, err := LookupInvite(d, payload.Inv)
	if err != nil {
		return nil, err
	}
	if invite.UsedByNodeID != req.NodeID {
		// Token doesn't match the node. Either the
		// token is being used by a different node
		// (suspicious) or the node was force-removed
		// and a new node joined with a different
		// invite.
		return nil, fmt.Errorf("token's invite is bound to node %q, not %q", invite.UsedByNodeID, req.NodeID)
	}

	// 3. Update last_seen_at + auto-transition state
	// pending → ready on the first heartbeat.
	// (We don't transition ready → failed here; the
	// HA elector handles failed based on missed
	// heartbeats. Phase 3.)
	now := time.Now().UTC()
	var state string
	var lastSeen time.Time
	err = d.QueryRow(`
		UPDATE cluster_node
		   SET last_seen_at = $1,
		       state = CASE
		           WHEN state = 'pending' THEN 'ready'
		           ELSE state
		       END
		 WHERE id = $2
		RETURNING state, last_seen_at
	`, now, req.NodeID).Scan(&state, &lastSeen)
	if err == sql.ErrNoRows {
		return nil, ErrHeartbeatNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}
	return &HeartbeatResponse{
		NodeID:               req.NodeID,
		State:                state,
		LastSeenAt:           lastSeen.Unix(),
		NextHeartbeatSeconds: 30,
		HeartbeatsUntilStale: 3, // 3 missed → failed (HA elector)
	}, nil
}

// readDBBootstrap returns (dsn_template, dbname, username)
// from the cluster_database row for the given cluster, or
// empty strings if not configured. The new node uses
// these to bootstrap its own pgxpool (.env).
func readDBBootstrap(d *sql.DB, clusterID string) (dsnTpl, dbName, dbUser string) {
	if d == nil || clusterID == "" {
		return
	}
	row := d.QueryRow(`
		SELECT COALESCE(dsn_template, ''),
		       COALESCE(dbname, ''),
		       COALESCE(username, '')
		  FROM cluster_database
		 WHERE id = $1
	`, clusterID)
	if err := row.Scan(&dsnTpl, &dbName, &dbUser); err != nil {
		// not configured — the new node will discover
		// its own DSN via its own entrypoint.sh
		return "", "", ""
	}
	return
}

// readPrimaryHost returns the hostname of the cluster's
// current primary (the cluster_node row whose id equals
// cluster_database.primary_node_id). Returns an empty
// string + nil if no primary is configured yet (the
// admin hasn't run `skygate init` or the primary_node_id
// is NULL for any other reason).
//
// Used by B212 to compute the substituted DSN — the
// new node needs a complete DSN (not a template with
// %s) to point its own pgxpool at the cluster's PG.
func readPrimaryHost(d *sql.DB, clusterID string) string {
	if d == nil || clusterID == "" {
		return ""
	}
	var host string
	err := d.QueryRow(`
		SELECT COALESCE(n.hostname, '')
		  FROM cluster_database cd
		  LEFT JOIN cluster_node n ON n.id = cd.primary_node_id
		 WHERE cd.id = $1
	`, clusterID).Scan(&host)
	if err != nil {
		return ""
	}
	return host
}

// substituteDSNTemplate replaces the single %s in a
// DSN template with the primary's reachable hostname.
// Returns the template unchanged if there's no %s
// (some setups hardcode the host) or an empty string
// if host is empty (the caller should treat the result
// as "no DSN bootstrap available").
//
// B212: the standby's `skygate join` needs a ready-to-
// use DSN to bootstrap its own pgxpool. Pre-B212 the
// standby got only the template (with %s unsubstituted)
// and had to know the primary's hostname out-of-band.
func substituteDSNTemplate(tpl, host string) string {
	if tpl == "" {
		return ""
	}
	if !strings.Contains(tpl, "%s") {
		// No placeholder — the host is already baked
		// in (e.g. "postgres://...@localhost:5432/...").
		// Return the template as-is.
		return tpl
	}
	if host == "" {
		// Template wants substitution but we have no
		// host. Return empty (the caller logs a clear
		// "no primary host" warning and falls back to
		// the standby's .env DSN).
		return ""
	}
	return strings.Replace(tpl, "%s", host, 1)
}

// hostnamesEqual compares two hostnames case-
// insensitively (DNS is case-insensitive in the
// lookup sense, even if the underlying records are
// case-sensitive). Whitespace is trimmed.
func hostnamesEqual(a, b string) bool {
	a = trimSpaceASCII(a)
	b = trimSpaceASCII(b)
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func trimSpaceASCII(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// parseRolesField parses the comma-sep roles field from
// the join request. Empty input → nil. Trims spaces.
// Deduplicates (so "skygate,skygate" → ["skygate"]).
func parseRolesField(s string) []string {
	if s == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range splitComma(s) {
		p = trimSpaceASCII(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// splitComma is a small CSV splitter that respects
// quoted segments (so "skygate, \"pat,ern\"" works).
// For our use case (short role lists) a naive split
// is fine.
func splitComma(s string) []string {
	var out []string
	var cur []byte
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			out = append(out, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, c)
		}
	}
	out = append(out, string(cur))
	return out
}
