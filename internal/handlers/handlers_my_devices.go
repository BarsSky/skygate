package handlers

// handlers_my_devices.go — GET /my/devices: list the current user's
// devices plus public/exit nodes. Lazy-backfills node_owner_map from
// headscale's preAuthKey history on every load so the user sees their
// tagged devices immediately.
// Extracted from handlers.go.

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// GetMyDevices lists the current user's own devices plus the
// tailnet's public/exit nodes. Performs a lazy backfill of
// node_owner_map from headscale's preAuthKey history so the user
// sees their tagged devices on the first /my/devices load.
func (a *App) GetMyDevices(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	var hsUserID sql.NullInt64
	var username string
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserHSByID
	hsUserID, username, _ = db.GetUserHSByID(a.DB, c.UserID)

	// Get all nodes (cached). Reuse them for both my-nodes (filter by user)
	// and public nodes (filter by tag/exit) - one HTTP call to headscale
	// instead of two.
	// 2026-07-15: v0.12.0 — route to the user's own control plane.
	// The device list reflects the user's tailnet, not the
	// operator's primary one.
	t0 := time.Now()
	all, _ := a.HSForUser(c.UserID).ListAllNodes()

	// Lazy-backfill node_owner_map from headscale's preAuthKey history.
	// When a user creates a preauth key in /my/devices, we save its
	// headscale ID. When that key is later used to register a node,
	// headscale's API exposes node.PreAuthKey.ID. Match them and
	// snapshot the (node -> user) link in node_owner_map. This is the
	// ONLY way to recover ownership for nodes that headscale has
	// reassigned to the synthetic "tagged-devices" user because of
	// tag:private. We do this here, on the user's first /my/devices
	// load, so the same fix happens for every node the user owns -
	// without scanning the headscale DB up front.
	if c.UserID != 0 {
		a.backfillNodeOwnership(a.DB, all, c.UserID, username)
	}

	// headscale reassigns ownership to a synthetic "tagged-devices" user
	// whenever a tag is applied, so we cannot rely on the live user_id
	// alone. We keep a snapshot of the original owner in node_owner_map
	// and union both sources to compute "my devices".
	type myNodeRow struct {
		ID       string
		Hostname string
		IP       string
		Online   bool
		LastSeen string
		UserName string
		IsPublic bool
		Source   string
	}
	mySet := map[string]bool{}
	var myNodesList []myNodeRow
	for _, n := range all {
		if hsUserID.Valid && username != "" && n.UserName == username {
			mySet[n.ID] = true
			ip := ""
			if len(n.IPAddresses) > 0 {
				ip = n.IPAddresses[0]
			}
			myNodesList = append(myNodesList, myNodeRow{
				ID: n.ID, Hostname: n.Hostname, IP: ip,
				Online: n.Online, LastSeen: n.LastSeen,
				UserName: n.UserName, IsPublic: n.IsPublicView(),
				Source: "live",
			})
		}
	}
	if username != "" {
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.ListNodeOwnerNodeIDsByUsername.
		snapIDList, _ := db.ListNodeOwnerNodeIDsByUsername(a.DB, username)
		// Build a set for O(1) membership test. The list is small
		// (a user's owned devices) but a map keeps the lookups in
		// the inner loop tidy.
		snapIDs := map[string]bool{}
		for _, id := range snapIDList {
			snapIDs[id] = true
		}
		for _, n := range all {
			if !snapIDs[n.ID] || mySet[n.ID] {
				continue
			}
			ip := ""
			if len(n.IPAddresses) > 0 {
				ip = n.IPAddresses[0]
			}
			myNodesList = append(myNodesList, myNodeRow{
				ID: n.ID, Hostname: n.Hostname, IP: ip,
				Online: n.Online, LastSeen: n.LastSeen,
				UserName: n.UserName, IsPublic: n.IsPublicView(),
				Source: "snapshot",
			})
		}
	}

	publicNodes := []headscale.NodeView{}
	for _, n := range all {
		if n.IsExitNode || n.IsPublicView() {
			publicNodes = append(publicNodes, n)
		}
	}

	log.Printf("DBG GetMyDevices fetch took %v nodes=%d my=%d public=%d", time.Since(t0), len(all), len(myNodesList), len(publicNodes))

	// 2026-07-21: v0.22.3 — read the user's subnet row
	// (denormalized on portal_users) so the /my/devices page
	// can show "Your personal subnet: 10.0.<uid>.0/24 (active)"
	// without an extra JOIN. Backfill above may have just
	// flipped the status to active (the SyncStatus call in
	// backfillNodeOwnership), so the value here is fresh.
	var subnetCIDR, subnetStatus string
	_ = a.DB.QueryRow(
		`SELECT subnet_cidr, subnet_status FROM portal_users WHERE id = ?`, c.UserID,
	).Scan(&subnetCIDR, &subnetStatus)

	a.renderWithLayout(w, r, "user/devices.html", c, map[string]any{
		"MyNodes":      myNodesList,
		"PublicNodes":  publicNodes,
		"HasMyNodes":   len(myNodesList) > 0,
		"SubnetCIDR":   subnetCIDR,
		"SubnetStatus": subnetStatus,
	})
}
