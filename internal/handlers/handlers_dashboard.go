package handlers

import (
	"net/http"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// ---------- DASHBOARD ----------

// PreauthKeyStats breaks down a user's preauth keys by lifecycle state.
// Total == Used + Active + Expired. Active means "still usable right now":
// unused AND expiration (if set) is in the future. Expired means unused
// but past its expiration. Used means a headscale node consumed it.
//
// Moved from handlers_derp.go during Этап 8 — this type is dashboard-
// specific (used by TailnetMetrics.MyPreauthKeys) and doesn't belong
// in the DERP file.
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
	ActiveDERP     string
	// User-scoped metrics (populated when called with a username)
	MyTotalNodes     int
	MyOnlineNodes    int
	MyExitNodesCount int
	// MyPreauthKeys is a 3-way split (used/active/expired). Empty
	// when not a per-user call.
	MyPreauthKeys PreauthKeyStats
}

func (a *App) computeTailnetMetrics(myUsername string, myUserID int64) TailnetMetrics {
	m := TailnetMetrics{}
	nodes, _ := a.HS.ListAllNodes()
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
		a.backfillNodeOwnership(a.DB, nodes, myUserID, myUsername)
	}
	if myUsername != "" {
		// Use a set of node IDs the user owns, sourced from
		// node_owner_map.
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.ListNodeOwnerNodeIDsByUsername.
		owned := map[string]bool{}
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(a.DB, myUsername)
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
	users, _ := a.HS.ListUsers()
	m.UsersCount = len(users)
	// Preauth split is per-user; admins see zero (their own key history
	// is admin tooling, not a per-user metric).
	if myUserID != 0 {
		m.MyPreauthKeys = a.countMyPreAuthKeys(myUserID, nodes)
	}
	m.ActiveDERP = "waw" // could be parsed from netcheck but kept simple here
	return m
}

func (a *App) GetDashboard(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Look up the headscale username for this portal user (may be empty for
	// brand-new users who haven't registered a device yet).
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserNameByID
	hsUserName, _ := db.GetUserNameByID(a.DB, c.UserID)
	// Admins see whole-tailnet metrics; users see only their own.
	scope := ""
	if !c.IsAdmin && hsUserName != "" {
		scope = hsUserName
	}
	a.renderWithLayout(w, r, "dashboard.html", c, map[string]any{
		"TailnetMetrics": a.computeTailnetMetrics(scope, c.UserID),
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
func (a *App) countMyPreAuthKeys(myUserID int64, nodes []headscale.NodeView) PreauthKeyStats {
	var s PreauthKeyStats
	if myUserID == 0 {
		return s
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
	rows, err := db.ListPreauthKeysByUser(a.DB, myUserID)
	if err != nil {
		return s
	}
	for _, k := range rows {
		s.Total++
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
			s.Used++
		case k.ExpiresAt > 0 && k.ExpiresAt <= now:
			s.Expired++
		default:
			s.Active++
		}
	}
	return s
}
