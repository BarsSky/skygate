package my

// dashboard.go — /dashboard route (handlers moved from
// internal/handlers/handlers_dashboard.go).
//
// The dashboard is the user-facing landing page. It renders
// a small summary of the tailnet: total nodes, online nodes,
// exit-node count, users count, and (for non-admins) the
// user's own node counts + a 3-way preauth-key split
// (used / active / expired).
//
// refactor-v0.30 Phase B step 6d (2026-07-29): moved from
// internal/handlers/handlers_dashboard.go (198 lines).

import (
	"context"
	"log"
	"net/http"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// ---------- DASHBOARD TYPES ----------

// PreauthKeyStats breaks down a user's preauth keys by lifecycle state.
// Total == Used + Active + Expired. Active means "still usable right now":
// unused AND expiration (if set) is in the future. Expired means unused
// but past its expiration. Used means a headscale node consumed it.
type PreauthKeyStats struct {
	Total   int
	Used    int
	Active  int
	Expired int
}

// TailnetMetrics is a small summary of the tailnet for the dashboard hero.
// For admin: shows the whole tailnet. For users: shows their own devices
// and only the public/exit nodes they're allowed to see.
type TailnetMetrics struct {
	TotalNodes     int
	OnlineNodes    int
	ExitNodesCount int
	UsersCount     int
	// B235: ActiveDERP is the region_code of the
	// lowest-latency healthy DERP (own first, then
	// public). ActiveDERPLatencyMs is its current
	// probe latency. ActiveDERPRegionID is the
	// Tailscale-assigned numeric ID (matches the
	// "ID" column on /admin/derp/dashboard). All
	// three are empty/zero when no healthy DERP
	// exists in derp_health yet (the cron runs every
	// 5 min; right after skygate start the table is
	// empty for the first tick).
	//
	// Pre-B235 these were a hardcoded "waw" /
	// "could be parsed from netcheck but kept simple
	// here" — which produced the confusing
	// situation where the dashboard hero showed
	// "DERP WAW" but /admin/derp/dashboard showed
	// only the own DERP. B235 makes the two
	// consistent: the hero shows the actual current
	// recommendation, with the same source of truth
	// as the admin dashboard.
	ActiveDERP           string
	ActiveDERPLatencyMs  int
	ActiveDERPRegionID   int
	// User-scoped metrics (populated when called with a username)
	MyTotalNodes     int
	MyOnlineNodes    int
	MyExitNodesCount int
	// MyPreauthKeys is a 3-way split (used/active/expired). Empty
	// when not a per-user call.
	MyPreauthKeys PreauthKeyStats
}

// computeTailnetMetrics takes a *headscale.Client so the
// caller can route the API call to either the global
// headscale (admin view) or a per-user headscale (user
// view). v0.12.0: the dashboard renders the user's own
// headscale plane rather than always the operator's
// primary one. See GetDashboard for the routing decision.
func (s *Service) computeTailnetMetrics(myUsername string, myUserID int64, hs *headscale.Client) TailnetMetrics {
	m := TailnetMetrics{}
	nodes, _ := hs.ListAllNodes()
	m.TotalNodes = len(nodes)
	for _, n := range nodes {
		if n.Online {
			m.OnlineNodes++
		}
		if n.IsExitNode {
			m.ExitNodesCount++
		}
	}
	// Per-user metrics: for non-admin users, count nodes via node_owner_map
	// (same source /my/devices uses) rather than n.UserName, because
	// headscale reassigns tagged nodes to a synthetic "tagged-devices"
	// user and the live user_id link is lost. The backfill that runs in
	// /my/devices also fires from here, so the dashboard sees the same
	// set the moment the user lands on the page.
	if myUserID != 0 {
		s.BackfillNodeOwnership(s.DB, nodes, myUserID, myUsername)
	}
	if myUsername != "" {
		// Use a set of node IDs the user owns, sourced from
		// node_owner_map.
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.ListNodeOwnerNodeIDsByUsername.
		owned := map[string]bool{}
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), myUsername)
		for _, nid := range snapIDs {
			owned[nid] = true
		}
		// Plus any node still showing the live user name (untagged nodes).
		for _, n := range nodes {
			if n.UserName == myUsername {
				owned[n.ID] = true
			}
		}
		for _, n := range nodes {
			if !owned[n.ID] {
				continue
			}
			m.MyTotalNodes++
			if n.Online {
				m.MyOnlineNodes++
			}
			if n.IsExitNode {
				m.MyExitNodesCount++
			}
		}
	}
	users, _ := hs.ListUsers()
	m.UsersCount = len(users)
	// Preauth split is per-user; admins see zero (their own key history
	// is admin tooling, not a per-user metric).
	if myUserID != 0 {
		m.MyPreauthKeys = s.countMyPreAuthKeys(myUserID, nodes)
	}
	// B235: pull the actual lowest-latency healthy DERP
	// from derp_health (the same source the admin
	// dashboard uses). Empty string + zero latency
	// when the table hasn't been populated yet
	// (first cron tick after skygate start, or
	// long-running outage). The hero renders "—" in
	// that case instead of a stale hardcoded value.
	if code, lat, rid, ok := s.bestHealthyDERP(); ok {
		m.ActiveDERP = code
		m.ActiveDERPLatencyMs = lat
		m.ActiveDERPRegionID = rid
	}
	return m
}

// bestHealthyDERP returns the region_code + latency + region_id
// of the lowest-latency healthy DERP from derp_health. Own
// DERP'ы are preferred (is_own=1) on equal latency.
// Returns ok=false when the table has no healthy rows yet
// (the cron runs every 5 min; the first tick after
// skygate start leaves the table empty until ProbeAll
// writes the first batch of results).
//
// B235: this is what the dashboard hero uses to
// show "DERP WAW 105 ms" instead of the pre-B235
// hardcoded "waw" placeholder.
//
// B235.2: the bundled controlplane DERP (region_id=901,
// is_own=1) has region_code='' because the operator
// didn't set one in derp_relays — it's the Tailscale
// control plane's fallback, not a real geographic
// region. Without a fallback the hero rendered "—"
// even though the row had a healthy latency. This
// function now falls back to a short label derived
// from the host: "controlplane" → "cdn", or the
// first label of the hostname otherwise. The fallback
// is local to this function (the DB stays the source
// of truth — region_code='' remains the operator's
// choice, and a future /admin/derp edit can set a
// real code).
func (s *Service) bestHealthyDERP() (code string, latencyMs int, regionID int, ok bool) {
	rows, err := s.dbc().QueryContext(context.Background(), `
		SELECT region_id, COALESCE(region_code, ''),
		       COALESCE(host, ''), latency_ms
		  FROM derp_health
		 WHERE healthy = 1
		   AND latency_ms > 0
		 ORDER BY is_own DESC, latency_ms ASC
		 LIMIT 1
	`)
	if err != nil {
		log.Printf("dashboard: bestHealthyDERP query: %v", err)
		return "", 0, 0, false
	}
	defer rows.Close()
	if rows.Next() {
		var rid int
		var c, host string
		var lat int
		if err := rows.Scan(&rid, &c, &host, &lat); err == nil {
			// B235.2: fall back to a short label when
			// region_code is empty. The bundled 901
			// (controlplane.tailscale.com) is the most
			// common case — show "cdn" so the hero
			// displays something useful ("cdn 108 ms")
			// instead of "—".
			if c == "" {
				c = shortHostLabel(host)
			}
			return c, lat, rid, true
		}
	}
	return "", 0, 0, false
}

// shortHostLabel returns a short display label for a
// DERP host. The bundled controlplane DERP (host =
// "controlplane.tailscale.com") maps to "cdn".
// Other hosts (e.g. "derp22b.tailscale.com") get
// their first label (e.g. "derp22b") which is what
// the admin dashboard's `.Name` pill would show.
//
// Pure function — easy to unit test.
func shortHostLabel(host string) string {
	if host == "" {
		return ""
	}
	if host == "controlplane.tailscale.com" {
		return "cdn"
	}
	// Split on the first dot.
	for i, r := range host {
		if r == '.' {
			return host[:i]
		}
	}
	return host
}

// GetDashboard renders the /dashboard page. Admin sees whole-tailnet
// metrics; non-admin sees their own subset (nodes + preauth split)
// routed through the per-user headscale plane.
func (s *Service) GetDashboard(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Look up the headscale username for this portal user (may be empty for
	// brand-new users who haven't registered a device yet).
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserNameByID
	hsUserName, _ := db.GetUserNameByID(s.dbc(), c.UserID)
	// Admins see whole-tailnet metrics; users see only their own.
	// 2026-07-15: v0.12.0 — route the headscale API call to the
	// user's own control plane when one is configured. Admins
	// (who have the global view) stay on HSGlobal(). A non-admin
	// with no per-user override also gets HSGlobal() — same as
	// v0.11.x behaviour.
	hs := s.Backend.HSGlobalFn()
	if !c.IsAdmin {
		hs = s.Backend.HSForUserFn(c.UserID)
	}
	scope := ""
	if !c.IsAdmin && hsUserName != "" {
		scope = hsUserName
	}
	s.Backend.RenderWithLayout(w, r, "dashboard.html", c, map[string]any{
		"TailnetMetrics": s.computeTailnetMetrics(scope, c.UserID, hs),
	})
}

// countMyPreAuthKeys classifies every preauth key the user has been
// issued. preauth_keys.user_id references portal_users.id (NOT headscale
// username). The split lets the dashboard show "1 used, 0 active, 1
// expired" instead of a single number that requires the user to
// remember what each key was for.
//
// Side effect: a key is considered "used" when either our local
// `used` column is set OR any headscale node currently lists that
// key as its preAuthKey. The node-side check is the source of truth
// - if the node is gone (deleted, expired server-side) but our
// local row was never flipped, we flip it here. This keeps the
// counter honest without a separate garbage-collection job.
func (s *Service) countMyPreAuthKeys(myUserID int64, nodes []headscale.NodeView) PreauthKeyStats {
	var st PreauthKeyStats
	if myUserID == 0 {
		return st
	}
	// Collect headscale preAuthKey IDs currently attached to any node.
	// These are authoritative "used" keys.
	hsUsedKeyIDs := map[string]bool{}
	for _, n := range nodes {
		if n.PreAuthKeyID != "" {
			hsUsedKeyIDs[n.PreAuthKeyID] = true
		}
	}
	now := time.Now().Unix()
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.ListPreauthKeysByUser.
	// The full row (including Key, CreatedAt) is loaded but only
	// HeadscalePreauthID, Used, ExpiresAt are used here. The extra
	// columns are tiny; having one read function is worth it.
	rows, err := db.ListPreauthKeysByUser(s.dbc(), myUserID)
	if err != nil {
		return st
	}
	for _, k := range rows {
		st.Total++
		// Determine the authoritative used state. Prefer the live
		// headscale signal (node.preAuthKey.id) over the local flag,
		// so a missing local flip doesn't keep a key listed as active
		// once the device exists. We DO NOT clear the local flag here
		// - that's a side-effect the user should opt into via a
		// separate sync job; for the counter, just trust headscale.
		isUsed := k.Used
		if k.HeadscalePreauthID != "" && hsUsedKeyIDs[k.HeadscalePreauthID] {
			isUsed = true
		}
		switch {
		case isUsed:
			st.Used++
		case k.ExpiresAt > 0 && k.ExpiresAt <= now:
			st.Expired++
		default:
			st.Active++
		}
	}
	return st
}
