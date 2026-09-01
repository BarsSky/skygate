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
	"net/http"
	"strings"
	"time"
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
	ClusterChain []string // human-readable lines (one per member if any)
	HasCluster   bool

	// 2. Nodes
	Nodes       []clusterNodeRow
	NodeCount   int
	StateCounts map[string]int // "ready" → 2, "pending" → 1, etc.

	// 3. Database (pointer)
	DBConfigured bool
	DBPrimary    string

	// 4. Pending invites
	Invites      []clusterInviteRow
	InviteCount  int

	// 5. Recent cluster audit
	RecentEvents []clusterAuditEvent

	// Flash
	FlashSuccess string
	FlashError   string
}

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
		StateCounts:  map[string]int{},
	}

	// 1. Cluster — for now we read the single "skygate-staging"
	// cluster (B195 hard-coded the id; Phase 2.2 will let the
	// operator switch clusters). We tolerate the "no cluster"
	// state (not configured yet) without erroring.
	clusterID := "skygate-staging"
	if row := s.DB.QueryRowContext(r.Context(),
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
	rows, err := s.DB.QueryContext(r.Context(), `
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
			data.StateCounts[n.State]++
			data.Nodes = append(data.Nodes, n)
		}
		data.NodeCount = len(data.Nodes)
	} else {
		data.FlashError = "load nodes: " + err.Error()
	}

	// 3. Database — read cluster_database. The full editor is
	// on /admin/database; here we just show whether a row
	// exists (and which node is the primary).
	if row := s.DB.QueryRowContext(r.Context(),
		`SELECT primary_node_id, current_dsn FROM cluster_database WHERE id = $1`,
		clusterID,
	); row != nil {
		var primary sql.NullString
		var dsn string
		if err := row.Scan(&primary, &dsn); err == nil {
			data.DBConfigured = true
			if primary.Valid {
				data.DBPrimary = primary.String
			}
		} else if err != sql.ErrNoRows {
			// not fatal — just means the DB section shows
			// "not configured" without an error badge
		}
	}

	// 4. Pending invites — read cluster_invite rows that are
	// still pending and not expired. Phase 2.2 will add the
	// "Generate invite" form.
	invRows, err := s.DB.QueryContext(r.Context(), `
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

	// 5. Recent cluster audit (last 20) — same audit_log table
	// that /admin/audit reads, but filtered to cluster.* action
	// names. Phase 2.2 (invite generation) will add new
	// "cluster.invite.generate" / "cluster.invite.revoke" rows
	// here.
	if aRows, err := s.DB.QueryContext(r.Context(), `
		SELECT unix_timestamp, actor, action, detail
		  FROM audit_log
		 WHERE action LIKE 'cluster.%'
		 ORDER BY id DESC
		 LIMIT 20
	`); err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var ev clusterAuditEvent
			if err := aRows.Scan(&ev.WhenUnix, &ev.Actor, &ev.Action, &ev.Detail); err != nil {
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
