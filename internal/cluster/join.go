// Package cluster — join.go owns the join flow: a new
// node uses an sgn1 invite token to register itself
// in the cluster, and then sends periodic heartbeats
// to keep its cluster_node row fresh.
//
// v1.5.0+ / B201 — Phase 2.3 of
// docs/ha.md.
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
	DSNTemplate   string `json:"dsn_template"` // raw template (the %s is unsubstituted) — kept for backward compat with the B200 / B201 clients that want to do their own substitution
	DSN           string `json:"dsn"`          // B212: the template with %s substituted by the primary's reachable hostname (e.g. Tailscale hostname). Empty if no primary is configured (the standby falls back to its own .env DSN).
	PrimaryHost   string `json:"primary_host"` // B212: the hostname we substituted into DSN. Useful for the standby to log + verify the DSN points where it expects.
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

	// 5. Adoption: a cluster_node row for THIS hostname may already exist —
	//    created by the B223 discovery pass ("node-disc-<hostname>",
	//    skygate_version = "(discovered via Tailscale)"), by the panel's
	//    /admin/cluster/onboard action (AddNode, empty version), or by an
	//    earlier join. The natural key is (cluster_id, hostname), which has its
	//    own unique index (`idx_cluster_node_cluster_hostname` / PG's
	//    `cluster_node_cluster_id_hostname_key`), while `id` is a *different*
	//    unique key.
	//
	//    B365 (2026-10-08, live): the version of the JOIN must land on the row
	//    the operator sees. Measured on the reference standby the join's own
	//    upsert never fired at all — this branch used to mark the invite used
	//    and RETURN the existing row untouched, so the row kept
	//    "(discovered via Tailscale)" as its skygate_version while the
	//    heartbeat daemon kept its last_seen_at fresh, and /admin/cluster showed
	//    `id=node-disc-<host> … skygate_version=(discovered via Tailscale)` for a
	//    host that had really joined. The row is therefore ADOPTED here:
	//    refreshed in place, id preserved (the invite's used_by_node_id and the
	//    already-running heartbeat daemon both name it).

	// 6. Parse the roles (comma-sep), default to skygate-standby. Parsed BEFORE
	//    the row is adopted so a re-join can refresh the role set.
	roles := parseRolesField(req.Roles)
	if len(roles) == 0 {
		roles = []string{NodeRoleStandby}
	}
	now := time.Now().UTC()
	if existing, err := LookupNode(d, clusterID, req.Hostname); err == nil && existing != nil {
		if err := adoptNodeOnJoin(d, existing.ID, req, roles, now); err != nil {
			return nil, err
		}
		markInviteUsed(d, payload.Inv, existing.ID)
		auditNodeJoin(d, clusterID, existing.ID, req, roles, payload.Inv)
		return joinResponse(d, clusterID, existing.ID, req.Hostname), nil
	} else if err != nil && !errors.Is(err, ErrNodeNotFound) {
		return nil, fmt.Errorf("lookup node: %w", err)
	}

	// 7. No row for this hostname yet — create it in "pending" state. The id is
	//    derived from the invite so a re-join after a force-remove is a clean
	//    INSERT, not a flaky UPDATE. The conflict target is the row's NATURAL
	//    key (cluster_id, hostname) — the key a concurrent discovery tick would
	//    actually collide on — and the DO UPDATE clause makes such a race land
	//    the join's version/state on the discovered row instead of failing the
	//    whole join with a unique-violation. (Pre-B365 this read `ON CONFLICT
	//    (id)`, i.e. it named a key the discovery row never collides on.)
	//
	//    The DO UPDATE deliberately does NOT overwrite `id`: the row that
	//    collides keeps its own id, because an invite and a heartbeat daemon may
	//    already name it. Only the join-owned columns are refreshed.
	nodeID := "node-" + payload.Inv[:12]
	// B291: bind the timestamps through DialectKind.TimeValue — a raw
	// time.Time lands in a SQLite column as Go's String() form, which the
	// page readers could not decode before B291 (the row rendered "—").
	//
	// B363 (2026-10-08, live): these three comment lines used to sit INSIDE the
	// SQL literal — they followed the `+db.ActiveDialect().NowExpr()+`
	// concatenation, so PostgreSQL received
	//
	//     … last_seen_at = EXTRACT(epoch FROM now())
	//     	// B291: bind the timestamps through DialectKind.TimeValue — a raw
	//
	// and answered `insert node: ERROR: syntax error at or near ":" (SQLSTATE
	// 42601)`. The JOIN ENDPOINT THEREFORE NEVER WORKED on the production
	// dialect: every "create the cluster from the panel alone" attempt died
	// here with a 401 carrying that SQL text. Found by running the panel's own
	// onboarding block on a real second host, which is exactly what that flow
	// exists for.
	dialect := db.ActiveDialect()
	err = d.QueryRow(`
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
	`, nodeID, clusterID, req.Hostname, req.TailscaleIP,
		db.TextArrayLiteral(roles), req.SkygateVersion, dialect.TimeValue(now)).Scan(&nodeID)
	if err != nil {
		return nil, fmt.Errorf("insert node: %w", err)
	}

	// 8. Mark the invite as used (atomic with the node
	// INSERT — both inside the same transaction in a
	// future improvement; for now, sequential). The
	// INSERT above answers the id that actually owns the
	// (cluster_id, hostname) row, so the invite is bound
	// to the row the operator will see.
	markInviteUsed(d, payload.Inv, nodeID)

	// 9. B215: emit the node_join audit event. We use db.InsertClusterAudit
	//    (the canonical helper) so the JSONB shape is consistent with the
	//    failover events. Both join paths write it (see auditNodeJoin), so a join
	//    that ADOPTED a discovered row is no longer invisible in the per-node
	//    event history: before B365 that host produced no node_join event at all.
	auditNodeJoin(d, clusterID, nodeID, req, roles, payload.Inv)

	// 10. Bootstrap info (DSN template + substituted DSN) — the helper reads
	//     cluster_database, so both join paths return identical material.
	return joinResponse(d, clusterID, nodeID, req.Hostname), nil
}

// auditNodeJoin writes the B215 node_join cluster_audit row for one join.
//
// Best-effort on purpose: the cluster_node row is already committed, so a
// failure here must not abort the join — the operator just loses one audit row.
// The detail captures the join-relevant fields so /admin/ha's "Last 20 events"
// view can show the new node's metadata.
func auditNodeJoin(d *sql.DB, clusterID, nodeID string, req *JoinRequest, roles []string, inviteID string) {
	if d == nil {
		return
	}
	_, _ = db.InsertClusterAudit(d, clusterID, db.NodeJoin, nodeID, req.Hostname,
		fmt.Sprintf(`{"node_id":%q,"hostname":%q,"roles":%q,"tailscale_ip":%q,"skygate_version":%q,"invite_id":%q}`,
			nodeID, req.Hostname, strings.Join(roles, ","), req.TailscaleIP, req.SkygateVersion, inviteID))
}

// joinResponse assembles the bootstrap payload from the cluster's
// cluster_database row. Extracted (B365) so the adopt path and the
// fresh-insert path cannot drift: both must return the same DSN material for
// the same cluster.
func joinResponse(d *sql.DB, clusterID, nodeID, hostname string) *JoinResponse {
	dsnTpl, dbName, dbUser := readDBBootstrap(d, clusterID)
	primaryHost := readPrimaryHost(d, clusterID)
	return &JoinResponse{
		ClusterID:     clusterID,
		NodeID:        nodeID,
		Hostname:      hostname,
		DSNTemplate:   dsnTpl,
		DSN:           substituteDSNTemplate(dsnTpl, primaryHost),
		PrimaryHost:   primaryHost,
		DBName:        dbName,
		DBUsername:    dbUser,
		HeartbeatHint: 30,
	}
}

// adoptNodeOnJoin refreshes the EXISTING cluster_node row of the joining
// hostname (B365). It writes only the columns a join owns — the identity
// columns `id` and `cluster_id` are the caller's key and are never rewritten.
//
// state is forced to 'pending': the row it adopts may carry
// "(discovered via Tailscale)" from the B223 pass, or an older join's
// state='ready'. ApproveNode is the operator's gate afterwards, and a join that
// did not reset the state would let a node it never approved look approved.
func adoptNodeOnJoin(d *sql.DB, nodeID string, req *JoinRequest, roles []string, now time.Time) error {
	dialect := db.ActiveDialect()
	_, err := d.Exec(`
		UPDATE cluster_node
		   SET hostname = $2,
		       tailscale_ip = $3,
		       roles = `+dialect.CastTextArray("$4")+`,
		       state = 'pending',
		       skygate_version = $5,
		       joined_at = $6,
		       last_seen_at = $6
		 WHERE id = $1
	`, nodeID, req.Hostname, req.TailscaleIP,
		db.TextArrayLiteral(roles), req.SkygateVersion, dialect.TimeValue(now))
	if err != nil {
		return fmt.Errorf("adopt node: %w", err)
	}
	return nil
}

// markInviteUsed binds the invite to the node row that the join adopted or
// created. Idempotent (`used_at IS NULL` + COALESCE) and best-effort: the node
// row is already committed, so a failure here must not fail the join — the next
// heartbeat retries the binding.
func markInviteUsed(d *sql.DB, inviteID, nodeID string) {
	if d == nil || inviteID == "" || nodeID == "" {
		return
	}
	_, _ = d.Exec(`
		UPDATE cluster_invite
		   SET used_at = COALESCE(used_at, `+db.ActiveDialect().NowExpr()+`),
		       used_by_node_id = COALESCE(NULLIF(used_by_node_id, ''), $2)
		 WHERE id = $1 AND used_at IS NULL
	`, inviteID, nodeID)
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
	NodeID               string `json:"node_id"`
	State                string `json:"state"`
	LastSeenAt           int64  `json:"last_seen_unix"`
	NextHeartbeatSeconds int    `json:"next_heartbeat_seconds"`
	HeartbeatsUntilStale int    `json:"heartbeats_until_stale"`
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
	// B291: last_seen_at round-trips through the driver, so read it
	// as `any` and decode it — SQLite hands back a TEXT/INTEGER value
	// that cannot scan into *time.Time. Write it through
	// DialectKind.TimeValue so both backends store a shape both
	// decoders understand.
	var lastSeenRaw any
	err = d.QueryRow(`
		UPDATE cluster_node
		   SET last_seen_at = $1,
		       state = CASE
		           WHEN state = 'pending' THEN 'ready'
		           ELSE state
		       END
		 WHERE id = $2
		RETURNING state, last_seen_at
	`, db.ActiveDialect().TimeValue(now), req.NodeID).Scan(&state, &lastSeenRaw)
	if err == sql.ErrNoRows {
		return nil, ErrHeartbeatNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}
	lastSeen, _ := db.ParseDBTime(lastSeenRaw)
	if lastSeen.IsZero() {
		lastSeen = now
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
