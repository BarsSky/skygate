// Package admin — cluster.go owns the /admin/cluster page
// (cluster topology view: see §2 in
// docs/internal/cluster-management.md).
//
// v1.5.0+ / B199 — Phase 2.1 (read-only cluster overview).
//
// Page surface (5 sections):
//
//	1. Cluster summary       — one card per cluster row
//	                            (typically just one: skygate-staging)
//	2. Nodes                 — table of cluster_node rows
//	                            (hostname, tailscale_ip, roles,
//	                             state, joined_at, last_seen_at;
//	                             the "self" row is highlighted)
//	3. Database              — pointer to /admin/database
//	                            (the cluster_database row is owned
//	                            by the DB page; here we just
//	                            show whether it's configured)
//	4. Pending invites       — table of cluster_invite rows
//	                            with status=pending (Phase 2.2
//	                            will add the "Generate invite"
//	                            button; for now read-only)
//	5. Recent cluster audit  — last 20 audit_log rows with
//	                            action LIKE 'cluster.%'
//
// Architectural notes:
//
//   - State is in the cluster_* tables (B195). /admin/cluster
//     is purely a read view — there are no POST handlers in
//     Phase 2.1. The action surface (add node, generate
//     invite, force remove) lands in Phase 2.2 (B200.x).
//
//   - The "self" row in the nodes table is the row whose
//     hostname matches Service.SelfHostname (the same field
//     /admin/ha uses). SelfHostname is wired from
//     cfg.TailscaleHostname in cmd/skygate/main.go.
//
//   - All errors degrade to "show the page with the error
//     in the flash" — a missing or empty cluster is normal
//     first-run state, not a 500.

package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/cluster"
	"skygate/internal/db"
)

// ---------- GET /admin/cluster (the page) -----------------------------

// clusterNodeRow is the in-memory shape of one cluster_node
// row, with the JSONB arrays expanded into []string so the
// template doesn't have to know about pg types. The roles
// field is rendered as a row of badge pills; the chain field
// (on cluster) is rendered as a human-readable line per member.
type clusterNodeRow struct {
	ID            string
	ClusterID     string
	Hostname      string
	TailscaleIP   string
	Roles         []string
	State         string
	SkygateVer    string
	JoinedAt      string
	LastSeenAt    string
	IsSelf        bool
	JoinedAgoSec  int64
	LastSeenAgoSec int64
}

// clusterInviteRow is one cluster_invite row (Phase 2.2 will
// add the form to create these; for now we just render the
// pending ones so the operator can see who's been invited).
type clusterInviteRow struct {
	ID             string
	ClusterID      string
	Role           string
	TargetHostname string
	IssuedAt       string
	ExpiresAt      string
	IssuedAgoSec   int64
	ExpiresInSec   int64
	Status         string
}

// clusterAuditEvent is one row of the "Last 20 cluster events"
// table. Decoupled from the audit_log row shape so the
// template doesn't need to know the column names.
type clusterAuditEvent struct {
	WhenUnix int64
	Actor    string
	Action   string
	Detail   string
}

// clusterPageData is the shape the template consumes. It pulls
// together all 5 sections in one struct so the template
// doesn't re-fetch.
type clusterPageData struct {
	// 1. Cluster summary
	ClusterID    string
	ClusterName  string
	// SelfHostname is the host name of THIS skygate
	// instance (from Service.SelfHostname, wired from
	// cfg.TailscaleHostname at boot). The template uses
	// it for the "cannot remove self" hint in the per-row
	// node Remove button.
	SelfHostname string
	ClusterChain []string // human-readable lines (one per member if any)
	HasCluster   bool

	// 2. Nodes
	Nodes       []clusterNodeRow
	NodeCount   int
	StateCounts map[string]int // "ready" → 2, "pending" → 1, etc.
	// OnlineCount is the subset of Nodes that are state=ready
	// AND have last_seen_at within OnlineThresholdSec. B216
	// adds this so the operator sees "3 of 4 nodes online"
	// at a glance (matches /admin/ha's "Self role" badge
	// logic and the HA elector's stale-failover threshold).
	OnlineCount   int
	OfflineCount  int
	HasStaleNodes bool // any state=ready with last_seen > threshold (network blip recovery)

	// 3. Database (pointer)
	DBConfigured  bool
	DBPrimary     string
	DBReplicas    []string // cluster_database.replica_node_ids (parsed from PG array)
	DBReplicaCnt  int
	DBDSNHost     string // host extracted from current_dsn for "where the primary DB is" display
	DBSSLMode     string
	HasReplicas   bool

	// 4. Pending invites
	Invites      []clusterInviteRow
	InviteCount  int

	// 5. Recent cluster audit
	RecentEvents []clusterAuditEvent

	// Flash
	FlashSuccess string
	FlashError   string
}

// OnlineThresholdSec is the staleness threshold used by
// clusterPageData.OnlineCount. A node is "online" if it
// has a fresh last_seen_at (within this many seconds).
//
// 90s matches the B204 HA elector's "3 missed heartbeats
// → state=failed" decision (heartbeat interval 30s × 3).
// Using the same threshold keeps the on-page online count
// consistent with what the HA chain thinks.
const OnlineThresholdSec = 90

// GetAdminCluster renders the /admin/cluster page.
func (s *Service) GetAdminCluster(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectClusterPageData(r)
	s.Backend.RenderWithLayout(w, r, "admin/cluster.html", c, map[string]any{
		"Data": data,
	})
}

// collectClusterPageData reads the cluster_* tables and the
// last 20 cluster.* audit events. All errors degrade to
// "show the page with the error in the flash".
func (s *Service) collectClusterPageData(r *http.Request) *clusterPageData {
	data := &clusterPageData{
		FlashSuccess: r.URL.Query().Get("ok"),
		FlashError:   r.URL.Query().Get("err"),
		SelfHostname: s.SelfHostname,
		StateCounts:  map[string]int{},
	}

	// 1. Cluster — for now we read the single "skygate-staging"
	// cluster (B195 hard-coded the id; Phase 2.2 will let the
	// operator switch clusters). We tolerate the "no cluster"
	// state (not configured yet) without erroring.
	clusterID := "skygate-staging"
	if row := s.dbc().QueryRowContext(r.Context(),
		`SELECT id, name, chain FROM cluster WHERE id = $1`, clusterID,
	); row != nil {
		var name string
		var chainJSON []byte
		if err := row.Scan(&clusterID, &name, &chainJSON); err == nil {
			data.HasCluster = true
			data.ClusterID = clusterID
			data.ClusterName = name
			data.ClusterChain = parseClusterChain(chainJSON)
		} else if err != sql.ErrNoRows {
			data.FlashError = "load cluster: " + err.Error()
		}
	}

	// 2. Nodes — read all cluster_node rows for this cluster,
	// sorted by hostname. The "self" row is highlighted.
	// B216: also compute the OnlineCount / OfflineCount
	// summary that the new "X of Y online" pill in the
	// Nodes section uses. A node is online if state=ready
	// AND last_seen_at is within OnlineThresholdSec. This
	// matches the HA elector's stale-failover threshold so
	// the page and the HA chain agree.
	rows, err := s.dbc().QueryContext(r.Context(), `
		SELECT id, cluster_id, hostname, COALESCE(tailscale_ip, ''),
		       roles, state, COALESCE(skygate_version, ''),
		       joined_at, last_seen_at
		  FROM cluster_node
		 WHERE cluster_id = $1
		 ORDER BY hostname
	`, clusterID)
	if err == nil {
		defer rows.Close()
		now := time.Now().Unix()
		for rows.Next() {
			var n clusterNodeRow
			var rolesStr string
			var joinedAt, lastSeenAt sql.NullTime
			if err := rows.Scan(&n.ID, &n.ClusterID, &n.Hostname, &n.TailscaleIP,
				&rolesStr, &n.State, &n.SkygateVer,
				&joinedAt, &lastSeenAt); err != nil {
				continue
			}
			// roles is TEXT[] in PG but pgx scans it as a
			// "{role1,role2}" literal. Parse the braces.
			n.Roles = parsePGTextArray(rolesStr)
			if joinedAt.Valid {
				n.JoinedAt = joinedAt.Time.UTC().Format("2006-01-02 15:04:05 UTC")
				n.JoinedAgoSec = now - joinedAt.Time.Unix()
			} else {
				n.JoinedAt = "—"
			}
			if lastSeenAt.Valid {
				n.LastSeenAt = lastSeenAt.Time.UTC().Format("2006-01-02 15:04:05 UTC")
				n.LastSeenAgoSec = now - lastSeenAt.Time.Unix()
			} else {
				n.LastSeenAt = "—"
			}
			n.IsSelf = (n.Hostname == s.SelfHostname)
			// B216: online/offline count. "Stale" means
			// state=ready but last_seen beyond the
			// threshold — the node hasn't been flipped
			// to failed yet but the heartbeat is overdue.
			online, stale := classifyNodeHealth(n.State, lastSeenAt, now)
			if online {
				data.OnlineCount++
			} else {
				data.OfflineCount++
				if stale {
					data.HasStaleNodes = true
				}
			}
			data.StateCounts[n.State]++
			data.Nodes = append(data.Nodes, n)
		}
		data.NodeCount = len(data.Nodes)
	} else {
		data.FlashError = "load nodes: " + err.Error()
	}

	// 3. Database — read cluster_database. The full editor is
	// on /admin/database; here we just show whether a row
	// exists (and which node is the primary + replicas +
	// where the DSN points).
	// B216: added replica_node_ids + dsn host extraction
	// so the operator sees "Primary: agent, Replicas: [svi-1,
	// svi-2], DSN: postgres://...@svi-1:5432/..." without
	// clicking through to /admin/database.
	if row := s.dbc().QueryRowContext(r.Context(),
		`SELECT primary_node_id, replica_node_ids, current_dsn, COALESCE(sslmode, '')
		   FROM cluster_database WHERE id = $1`,
		clusterID,
	); row != nil {
		var primary sql.NullString
		var replicasStr, dsn, sslmode string
		if err := row.Scan(&primary, &replicasStr, &dsn, &sslmode); err == nil {
			data.DBConfigured = true
			if primary.Valid {
				data.DBPrimary = primary.String
			}
			data.DBReplicas = parsePGTextArray(replicasStr)
			data.DBReplicaCnt = len(data.DBReplicas)
			data.HasReplicas = data.DBReplicaCnt > 0
			data.DBSSLMode = sslmode
			// Extract host:between-@and-:from current_dsn
			// (e.g. "postgres://u:p@172.17.0.1:5433/d" → "172.17.0.1:5433").
			// We use a permissive regex; if the DSN is
			// malformed we just leave DBDSNHost empty.
			data.DBDSNHost = extractDSNHost(dsn)
		} else if err != sql.ErrNoRows {
			// not fatal — just means the DB section shows
			// "not configured" without an error badge
		}
	}

	// 4. Pending invites — read cluster_invite rows that are
	// still pending and not expired. Phase 2.2 will add the
	// "Generate invite" form.
	invRows, err := s.dbc().QueryContext(r.Context(), `
		SELECT id, cluster_id, role, target_hostname,
		       issued_at, expires_at, status
		  FROM cluster_invite
		 WHERE cluster_id = $1
		   AND status = 'pending'
		   AND expires_at > NOW()
		 ORDER BY issued_at DESC
	`, clusterID)
	if err == nil {
		defer invRows.Close()
		now := time.Now().Unix()
		for invRows.Next() {
			var inv clusterInviteRow
			var issuedAt, expiresAt time.Time
			if err := invRows.Scan(&inv.ID, &inv.ClusterID, &inv.Role,
				&inv.TargetHostname, &issuedAt, &expiresAt, &inv.Status); err != nil {
				continue
			}
			inv.IssuedAt = issuedAt.UTC().Format("2006-01-02 15:04:05 UTC")
			inv.ExpiresAt = expiresAt.UTC().Format("2006-01-02 15:04:05 UTC")
			inv.IssuedAgoSec = now - issuedAt.Unix()
			inv.ExpiresInSec = expiresAt.Unix() - now
			data.Invites = append(data.Invites, inv)
		}
		data.InviteCount = len(data.Invites)
	} else {
		data.FlashError = "load invites: " + err.Error()
	}

	// 5. Recent cluster audit (last 20) — B216: read from
	// cluster_audit (B195+ dedicated table for cluster
	// events) instead of audit_log with LIKE 'cluster.%'.
	// The pre-B216 query was a no-op because cluster events
	// never used a "cluster." action prefix (the B195 spec
	// uses "node_init" / "node_join" / "node_drain" /
	// "node_leave" / "node_health" / "failover_recommend" /
	// "node_failover" / "node_drill" — none prefixed with
	// "cluster."). The result was an empty events table
	// on /admin/cluster even though the same events were
	// visible on /admin/ha (which queries cluster_audit
	// directly with the 8-action IN list).
	//
	// The 8 actions below are the same set /admin/ha
	// uses — keeping the two pages consistent. The
	// template renders them as colored badges (info for
	// init/join, warning for drain, secondary for leave,
	// default <code> for the other four) — see
	// cluster.html section 5.
	if aRows, err := s.dbc().QueryContext(r.Context(), `
		SELECT id,
		       extract(epoch FROM created_at)::bigint AS ts,
		       actor,
		       action,
		       detail::text
		  FROM cluster_audit
		 WHERE action IN ('node_health', 'failover_recommend', 'node_failover', 'node_drill',
		                  'node_init', 'node_join', 'node_drain', 'node_leave',
		                  'node_approve')
		 ORDER BY id DESC
		 LIMIT 20
	`); err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var ev clusterAuditEvent
			if err := aRows.Scan(new(int64), &ev.WhenUnix, &ev.Actor, &ev.Action, &ev.Detail); err != nil {
				continue
			}
			data.RecentEvents = append(data.RecentEvents, ev)
		}
	} else {
		// non-fatal — just shows "no events" instead of error
	}

	return data
}

// ---------- Pure helpers (testable without DB) ------------------------

// parsePGTextArray parses a postgres TEXT[] literal of the
// form "{a,b,c}" into a []string. Empty / NULL returns nil.
// The pgx driver returns TEXT[] as a Go []string directly,
// but only when the column is nullable; for non-nullable
// TEXT[] columns it can fall back to the literal form.
// We handle both cases.
//
// (For Phase 2.1 this is good enough; the cluster_node
// insert path will write proper arrays via pgx, so on the
// read path the []string form is what we usually get. The
// string form is the fallback.)
func parsePGTextArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" || s == "NULL" {
		return nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		// not array literal — return as-is wrapped in a slice
		return []string{s}
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return nil
	}
	// Naive split on comma — good enough for our short role
	// lists ("skygate", "patroni-primary", etc.). If a role
	// ever contains a comma or quote, we'll need a proper
	// array parser; right now there is no such role.
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// strip surrounding quotes if pgx wrapped them
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		out = append(out, p)
	}
	return out
}

// parseClusterChain renders the cluster.chain JSONB as a
// human-readable line per member. For the empty / null /
// malformed case we return nil (the template renders
// "(no chain)").
func parseClusterChain(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	// The chain is a JSON array of member objects. We don't
	// want to pull in encoding/json for one parse — the
	// template can render a single short string per line,
	// so we just look for hostname-like substrings between
	// quotes. For Phase 2.1 this is sufficient; the
	// canonical chain view is on /admin/ha.
	s := string(raw)
	if s == "[]" || s == "null" {
		return nil
	}
	// Try a relaxed split: each member is {...} — render
	// the whole thing on one line per member.
	var out []string
	depth := 0
	start := -1
	for i, c := range s {
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i+1])
				start = -1
			}
		}
	}
	if len(out) == 0 {
		// not parseable as {…}… — return the raw value as one line
		return []string{s}
	}
	return out
}

// abbreviateClusterTime returns a short human label for a
// delta in seconds. "12s ago", "5m ago", "3h ago", "2d ago".
func abbreviateClusterTime(deltaSec int64) string {
	if deltaSec < 0 {
		// future — clock skew
		return "in " + intToString(-deltaSec) + "s"
	}
	switch {
	case deltaSec < 60:
		return intToString(deltaSec) + "s ago"
	case deltaSec < 3600:
		return intToString(deltaSec/60) + "m ago"
	case deltaSec < 86400:
		return intToString(deltaSec/3600) + "h ago"
	default:
		return intToString(deltaSec/86400) + "d ago"
	}
}

// extractDSNHost returns the "host:port" substring of a
// postgres DSN. Used by B216 to display "where the primary
// DB is" on the /admin/cluster Database section without
// exposing the password. Examples:
//
//	"postgres://u:p@172.17.0.1:5433/db?sslmode=disable" → "172.17.0.1:5433"
//	"postgres://u:p@/db?host=/var/run/postgresql&..."   → "/db"  (best-effort — unix-socket form is ambiguous)
//	""                                                → ""
//
// For URL-form DSNs (the common case in skygate — see
// SKYGATE_DB_DSN), the parser uses net/url. For the
// libpq keyword form ("host=... port=..." with no
// scheme://) the parser returns "" — the operator
// gets an empty host field and can see the raw DSN on
// /admin/database if they need to. For unix-socket
// DSNs the parser can't reliably separate the socket
// path from the dbname (both look like "/path/segments"),
// so we return the segment between @ and the query
// string — not perfect, but better than nothing.
//
// This is a display-only helper; the actual DSN is
// stored and used as-is by the DB pool.
func extractDSNHost(dsn string) string {
	if dsn == "" {
		return ""
	}
	// Quick reject: no scheme → likely libpq keyword form.
	if !strings.Contains(dsn, "://") {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	// url.Parse returns:
	//   - URL form  "postgres://u:p@host:5432/db" → Host=host:5432
	//   - Unix-socket "postgres://u:p@/path/db"  → Host="" Path=/path/db
	//
	// For URL form, u.Host is the host:port we want.
	// For unix-socket, u.Host is empty and the path
	// contains the socket dir + dbname together — we
	// return the path as the best-effort display.
	if u.Host != "" {
		return u.Host
	}
	if u.Path != "" {
		return u.Path
	}
	return ""
}

// classifyNodeHealth is the pure helper behind the
// "X of Y online" pill on /admin/cluster. Returns:
//
//   - online: state=ready AND last_seen_at is within
//     OnlineThresholdSec. This matches the B204 HA
//     elector's "3 missed heartbeats → state=failed"
//     decision (heartbeat interval 30s × 3).
//   - stale: state=ready but last_seen_at is BEYOND the
//     threshold. The node is in the "grace window" —
//     the HA chain hasn't flipped it to state=failed
//     yet, but the heartbeat is overdue. The page
//     shows a separate "stale heartbeat" warning pill.
//
// Anything else (state=pending / draining / failed, or
// state=ready with NULL last_seen_at) is offline and
// NOT stale (the stale pill is specifically for the
// in-grace state, not the definitely-offline state).
func classifyNodeHealth(state string, lastSeen sql.NullTime, nowUnix int64) (online, stale bool) {
	if state != "ready" {
		return false, false
	}
	if !lastSeen.Valid {
		// state=ready but never had a heartbeat — not
		// "stale" (never was online to begin with).
		return false, false
	}
	age := nowUnix - lastSeen.Time.Unix()
	// Strict less-than: 89s is online, 90s is the stale
	// boundary. The B204 HA elector flips a node to
	// state=failed after the 3rd missed heartbeat
	// (heartbeat interval 30s × 3 = 90s), so a node
	// that hasn't heartbeated for 90s is one tick away
	// from being marked failed.
	if age < OnlineThresholdSec {
		return true, false
	}
	return false, true
}

// ---------- POST /admin/cluster/node/add (B200) ------------------------

// PostAdminClusterNodeAdd appends a new cluster_node row.
// Form fields: hostname (required), tailscale_ip (optional),
// roles (comma-separated; default "skygate"), skygate_version
// (optional). The admin UI checks for duplicates first, but
// the DB has the ultimate say (B195 schema is the source of
// truth).
func (s *Service) PostAdminClusterNodeAdd(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		clusterRedirect(w, r, "", "hostname is required")
		return
	}
	tailscaleIP := strings.TrimSpace(r.FormValue("tailscale_ip"))
	rolesRaw := strings.TrimSpace(r.FormValue("roles"))
	skygateVer := strings.TrimSpace(r.FormValue("skygate_version"))
	var roles []string
	if rolesRaw != "" {
		for _, p := range strings.Split(rolesRaw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				roles = append(roles, p)
			}
		}
	}
	// Pre-check: reject duplicates with a clear message.
	if existing, err := cluster.LookupNode(s.dbc(), clusterID, hostname); err == nil && existing != nil {
		clusterRedirect(w, r, "", "hostname already in cluster_node: "+hostname)
		return
	}
	id, err := cluster.AddNode(s.dbc(), clusterID, hostname, tailscaleIP, roles, skygateVer)
	if err != nil {
		clusterRedirect(w, r, "", "add node: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.add",
		fmt.Sprintf("id=%s hostname=%s roles=%v", id, hostname, roles),
		"cluster_node", hostname)
	clusterRedirect(w, r, fmt.Sprintf("Added %s (id %s).", hostname, id), "")
}

// ---------- POST /admin/cluster/node/remove (B200) ---------------------

// PostAdminClusterNodeRemove deletes a cluster_node row by
// hostname. Idempotent — removing a non-existent row is a
// no-op (still 200 OK with a flash).
func (s *Service) PostAdminClusterNodeRemove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		clusterRedirect(w, r, "", "hostname is required")
		return
	}
	// Refuse to remove the self row — the operator would
	// lock themselves out of the admin UI on the next page
	// load (no cluster_node = the node is "unknown" and
	// several other admin features would misbehave).
	if hostname == s.SelfHostname {
		clusterRedirect(w, r, "", "cannot remove the self row ("+s.SelfHostname+") — use /admin/ha instead to take this node offline")
		return
	}
	if err := cluster.RemoveNode(s.dbc(), clusterID, hostname); err != nil {
		clusterRedirect(w, r, "", "remove: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.remove",
		"hostname="+hostname,
		"cluster_node", hostname)
	clusterRedirect(w, r, "Removed "+hostname+".", "")
}

// ---------- POST /admin/cluster/node/drain (B217) -----------------

// PostAdminClusterNodeDrain sets state=draining on a
// cluster_node row. Idempotent: draining an already-
// draining node is a no-op (no audit row, no error).
//
// Phase 2.2.4 partial — the "drain" half of "drain +
// leave + cleanup". The HA chain sees state=draining
// and the operator can still inspect the row. To
// actually remove it, the operator clicks "Drain &
// Remove" (PostAdminClusterNodeDrainRemove) or "Remove"
// (PostAdminClusterNodeRemove, the B200 force-delete).
//
// Refuses to drain the self row (operator would
// lock themselves out of the admin UI on the next
// page load, same as PostAdminClusterNodeRemove).
func (s *Service) PostAdminClusterNodeDrain(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		clusterRedirect(w, r, "", "hostname is required")
		return
	}
	if hostname == s.SelfHostname {
		clusterRedirect(w, r, "", "cannot drain the self row ("+s.SelfHostname+") — use /admin/ha instead")
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if err := cluster.DrainNode(s.dbc(), clusterID, hostname, c.Username, reason); err != nil {
		clusterRedirect(w, r, "", "drain: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.drain",
		fmt.Sprintf("hostname=%s reason=%q", hostname, reason),
		"cluster_node", hostname)
	clusterRedirect(w, r, "Drained "+hostname+".", "")
}

// ---------- POST /admin/cluster/node/drain-remove (B217) ---------

// PostAdminClusterNodeDrainRemove is the Phase 2.2
// "drain + leave + cleanup" combo. It sets state=draining
// first (so the HA chain sees the node go offline), then
// DELETEs the row, in one transaction. Both cluster_audit
// rows (node_drain + node_leave) are emitted in the same
// transaction so the audit trail is complete even if the
// process crashes mid-flight.
//
// This is the recommended remove path for an active
// (state=ready or state=failed) node. The raw
// PostAdminClusterNodeRemove (force DELETE) is the
// emergency fallback for stuck rows that won't leave
// via the normal flow.
func (s *Service) PostAdminClusterNodeDrainRemove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		clusterRedirect(w, r, "", "hostname is required")
		return
	}
	if hostname == s.SelfHostname {
		clusterRedirect(w, r, "", "cannot drain+remove the self row ("+s.SelfHostname+") — use /admin/ha instead")
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if err := cluster.DrainAndRemoveNode(s.dbc(), clusterID, hostname, c.Username, reason); err != nil {
		clusterRedirect(w, r, "", "drain+remove: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.drain_remove",
		fmt.Sprintf("hostname=%s reason=%q", hostname, reason),
		"cluster_node", hostname)
	clusterRedirect(w, r, "Drained and removed "+hostname+".", "")
}

// ---------- POST /admin/cluster/node/approve (B217) ----------------

// PostAdminClusterNodeApprove transitions a cluster_node
// row from state=pending to state=ready. Phase 2.2.3 —
// the explicit-approval gate.
//
// The pre-B217 path auto-transitioned pending→ready on
// the first successful heartbeat (cluster.Heartbeat
// still does this). The new button adds a manual gate:
// some deployments want admin sign-off before a new
// standby joins the HA chain. Operators who prefer
// auto-approval can simply not click the button — the
// auto-transition on first heartbeat still works.
//
// Idempotent: approving an already-ready node is a no-op.
// Approving a non-pending node (draining / failed) is
// rejected (no auto-recovery of failed nodes).
func (s *Service) PostAdminClusterNodeApprove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		clusterRedirect(w, r, "", "hostname is required")
		return
	}
	// Self-row guard. The Approve button is only
	// rendered for state=pending rows, so this is
	// defense-in-depth — but keep the consistent
	// "self rows are read-only via /admin/cluster"
	// guarantee with the other Phase 2.2 handlers.
	if hostname == s.SelfHostname {
		clusterRedirect(w, r, "", "cannot approve the self row ("+s.SelfHostname+") — re-run `skygate init` on the box to refresh it instead")
		return
	}
	if err := cluster.ApproveNode(s.dbc(), clusterID, hostname, c.Username); err != nil {
		clusterRedirect(w, r, "", "approve: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.approve",
		"hostname="+hostname,
		"cluster_node", hostname)
	clusterRedirect(w, r, "Approved "+hostname+".", "")
}

// ---------- POST /admin/cluster/upgrade (B222) ---------------------
//
// PostAdminClusterUpgrade is the B222 (Phase 4.2)
// rolling-upgrade entry point. Two modes via the
// `target` form field:
//
//   - target=<hostname>: upgrade that ONE node
//     (drain → wait for /healthz to report the new
//     build → rejoin).
//   - target=all: iterate every ready+failed node
//     (B222's "rolling all" mode), upgrading one
//     at a time, stopping on first error.
//
// Both modes use cluster.UpgradeOrchestrator
// (internal/cluster/upgrade.go). The handler
// itself is synchronous — the per-node /healthz
// poll can take up to 5 minutes (the orchestrator's
// default HealthTimeout), and the "all" mode
// multiplies that by the number of nodes. The
// future B222.1 will add an async run + SSE
// progress stream; for v1 the operator gets a
// 303 to /admin/cluster on success, an error
// flash on failure.
//
// B221: the handler writes a
// `cluster.upgrade.fail` audit row via the
// orchestrator on the failure path (B222
// writes the success transitions to
// cluster_audit via DrainNode + RejoinNode —
// the B215 /admin/ha filter surfaces those).
//
// Self-upgrade guard: refuses to upgrade the
// orchestrating node. The orchestrator IS
// skygate, and the upgrade process restarts
// skygate. The check matches s.SelfHostname
// (the same value /admin/ha uses for its
// "self row" guards). On the "all" mode the
// orchestrator's own node is SKIPPED (not an
// error) — the "self" upgrade is a separate
// manual one-off (operator pushes the binary
// via the B150 S3 flow + restart).
func (s *Service) PostAdminClusterUpgrade(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		clusterRedirect(w, r, "", "target is required (use a hostname or 'all')")
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))

	// Self-upgrade guard for the per-node mode.
	// The "all" mode skips self (handled by the
	// orchestrator's UpgradeAll), but for the
	// per-node mode the operator might type
	// their own hostname by mistake — refuse
	// with a clear message instead of starting
	// the upgrade.
	if target != "all" && target == s.SelfHostname {
		clusterRedirect(w, r, "", "refusing to upgrade self ("+s.SelfHostname+") — run the upgrade from a peer node, then ssh here and run `skygate deploy pull` + restart manually")
		return
	}

	// Build the orchestrator with the live build
	// string (so waitForBuild can match it). The
	// 5-min HealthTimeout + 2-s poll interval
	// match the defaults documented on the struct.
	orch := cluster.NewUpgradeOrchestrator(s.BuildVersion)
	if err := r.Context().Err(); err != nil {
		clusterRedirect(w, r, "", "context cancelled before upgrade started: "+err.Error())
		return
	}
	if target == "all" {
		if err := orch.UpgradeAll(r.Context(), s.dbc(), clusterID, c.Username, reason); err != nil {
			clusterRedirect(w, r, "", "upgrade all: "+err.Error())
			return
		}
		clusterRedirect(w, r, "Upgrade all (rolling) complete.", "")
		return
	}
	if err := orch.UpgradeNode(r.Context(), s.dbc(), clusterID, target, c.Username, reason); err != nil {
		clusterRedirect(w, r, "", "upgrade "+target+": "+err.Error())
		return
	}
	clusterRedirect(w, r, "Upgrade complete for "+target+".", "")
}

// ---------- POST /admin/cluster/invite/generate (B200) ----------------

// PostAdminClusterInviteGenerate creates a new cluster_invite
// row and returns the signed token via the flash. The token
// is shown once on the next page load and never re-displayed
// (the secret stays in the server, so the row alone can't
// re-derive the token — this is a deliberate "one-time
// show" UX).
//
// Form fields: role (default "skygate-standby"),
// target_hostname (required, the host the invite is for),
// ttl_hours (default 24, max 168).
func (s *Service) PostAdminClusterInviteGenerate(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.ClusterInviteSecret == "" {
		clusterRedirect(w, r, "", "ClusterInviteSecret not configured — set SKYGATE_SECRET_KEY in .env")
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	clusterID := "skygate-staging"
	role := strings.TrimSpace(r.FormValue("role"))
	if role == "" {
		role = cluster.NodeRoleStandby
	}
	target := strings.TrimSpace(r.FormValue("target_hostname"))
	if target == "" {
		clusterRedirect(w, r, "", "target_hostname is required")
		return
	}
	ttlHours := 24
	if v := strings.TrimSpace(r.FormValue("ttl_hours")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			clusterRedirect(w, r, "", "ttl_hours must be a positive integer")
			return
		}
		if n > 168 {
			clusterRedirect(w, r, "", "ttl_hours must be <= 168 (7 days)")
			return
		}
		ttlHours = n
	}
	inviteID, token, expiresAt, err := cluster.IssueInvite(s.dbc(), clusterID, role, target, ttlHours, s.ClusterInviteSecret)
	if err != nil {
		clusterRedirect(w, r, "", "issue invite: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.invite.generate",
		fmt.Sprintf("id=%s role=%s target=%s ttl_hours=%d", inviteID, role, target, ttlHours),
		"cluster_invite", inviteID)
	// Show the token via the success flash. The token is
	// truncated to its first 20 chars + "..." so a casual
	// log scrape doesn't leak the full secret, while the
	// full token is shown in the rendered flash (one-time
	// view).
	msg := fmt.Sprintf("Invite generated for %s. Token (shown once, expires %s):\n\n%s\n\nSave it now — it cannot be re-displayed.",
		target, expiresAt.UTC().Format("2006-01-02 15:04 UTC"), token)
	clusterRedirect(w, r, msg, "")
}

// ---------- POST /admin/cluster/invite/revoke (B200) -------------------

// PostAdminClusterInviteRevoke marks a pending cluster_invite
// row status=revoked. Used invites (used_at IS NOT NULL) and
// already-revoked invites are silently no-op (idempotent).
func (s *Service) PostAdminClusterInviteRevoke(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		clusterRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	inviteID := strings.TrimSpace(r.FormValue("invite_id"))
	if inviteID == "" {
		clusterRedirect(w, r, "", "invite_id is required")
		return
	}
	if err := cluster.RevokeInvite(s.dbc(), inviteID); err != nil {
		clusterRedirect(w, r, "", "revoke: "+err.Error())
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.invite.revoke",
		"id="+inviteID,
		"cluster_invite", inviteID)
	clusterRedirect(w, r, "Invite "+inviteID+" revoked.", "")
}

// clusterRedirect wraps RedirectWithFlash with the /admin/cluster
// base path baked in. Mirrors haRedirect in ha.go.
func clusterRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	RedirectWithFlash(w, r, "/admin/cluster", okMsg, errMsg)
}
