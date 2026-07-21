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

	// 2026-07-21: v0.22.3 — read the user's subnet row
	// (denormalized on portal_users) so the /my/devices page
	// can show "Your personal subnet: 10.0.<uid>.0/24 (active)"
	// without an extra JOIN. Backfill above may have just
	// flipped the status to active (the SyncStatus call in
	// backfillNodeOwnership), so the value here is fresh.
	// v0.25.0 — read it HERE (not later) so we can fill
	// the new "Mesh subnet" column in the device rows.
	var subnetCIDR, subnetStatus string
	_ = a.DB.QueryRow(
		`SELECT subnet_cidr, subnet_status FROM portal_users WHERE id = ?`, c.UserID,
	).Scan(&subnetCIDR, &subnetStatus)

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
		ID              string
		Hostname        string
		IP              string
		Online          bool
		LastSeen        string
		UserName        string
		IsPublic        bool
		Source          string
		Tags            []string
		AvailableRoutes []string
		ApprovedRoutes  []string
		// IsSubnetRouter is true when this node carries
		// tag:subnet-router. v0.24.1 — the /my/devices page
		// shows a dedicated "subnet router" badge for these
		// nodes (with the per-user CIDR they advertise) so
		// the user can tell at a glance whether their
		// LAN-bridge is up. Cheap to compute; cheaper than
		// the template scanning Tags.
		IsSubnetRouter bool
		IsExitNode     bool
		// MeshSubnet is the per-user virtual subnet the
		// device "belongs to" for mesh-share purposes
		// (e.g. "10.0.1.0/24 (skyadmin)"). Empty for
		// shared infrastructure nodes (tag:public /
		// tag:exit-node) — those are shared, not per-user.
		// v0.25.0.
		MeshSubnet string
		// IsShared is true when the node is a shared
		// infrastructure node (tag:public, tag:exit-node)
		// rather than the user's own tag:private device.
		// Used by the template to render a "shared" pill
		// instead of the per-user CIDR in the new "Mesh
		// subnet" column. v0.25.0.
		IsShared bool
	}
	mySet := map[string]bool{}
	var myNodesList []myNodeRow
	// hasTag returns true if the node carries the given tag.
	// Inline (not from internal/sidecar) so this file stays
	// free of cross-package imports for a small helper.
	hasTag := func(tags []string, want string) bool {
		for _, t := range tags {
			if t == want {
				return true
			}
		}
		return false
	}
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
				UserName:        n.UserName,
				IsPublic:        n.IsPublicView(),
				Source:          "live",
				Tags:            n.Tags,
				AvailableRoutes: n.AvailableRoutes,
				ApprovedRoutes:  n.ApprovedRoutes,
				IsSubnetRouter:  hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:      n.IsExitNode,
				MeshSubnet:      subnetCIDR,
				IsShared:         n.IsPublicView() || n.IsExitNode,
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
				UserName:        n.UserName,
				IsPublic:        n.IsPublicView(),
				Source:          "snapshot",
				Tags:            n.Tags,
				AvailableRoutes: n.AvailableRoutes,
				ApprovedRoutes:  n.ApprovedRoutes,
				IsSubnetRouter:  hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:      n.IsExitNode,
				MeshSubnet:      subnetCIDR,
				IsShared:         n.IsPublicView() || n.IsExitNode,
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

	// v0.25.0 — mesh visibility for the /my/devices
	// subnet card. We compute:
	//   1. mySharesTo     — who I've shared my /24 with
	//                         (grantee = them, grantor = me)
	//   2. sharesToMe      — who has shared their /24 with
	//                         me (grantor = them, grantee = me)
	//   3. myMeshMembers   — every user in any active mesh
	//                         I belong to (with their /24)
	//   4. meshCount       — how many active meshes I'm in
	// The UI uses (1) and (2) in the subnet card to show
	// "you've shared with X" / "Y is sharing with you",
	// and (3) in the mesh preview block.
	type shareInfo struct {
		Username string
		CIDR     string
	}
	var mySharesTo, sharesToMe, myMeshMembers []shareInfo

	if subnetCIDR != "" {
		// (1) mySharesTo: I (grantor) shared with someone (grantee).
		rows, err := a.DB.Query(`
			SELECT p.username, s.cidr
			  FROM user_subnet_shares sh
			  JOIN user_subnets s ON s.user_id = sh.grantor_user_id
			  JOIN portal_users p ON p.id = sh.grantee_user_id
			 WHERE sh.grantor_user_id = ? AND s.status != 'disabled'
			 ORDER BY p.username`, c.UserID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var s shareInfo
				if rows.Scan(&s.Username, &s.CIDR) == nil {
					mySharesTo = append(mySharesTo, s)
				}
			}
		}
		// (2) sharesToMe: someone (grantor) shared with me (grantee).
		rows2, err := a.DB.Query(`
			SELECT p.username, s.cidr
			  FROM user_subnet_shares sh
			  JOIN user_subnets s ON s.user_id = sh.grantor_user_id
			  JOIN portal_users p ON p.id = sh.grantor_user_id
			 WHERE sh.grantee_user_id = ? AND s.status != 'disabled'
			 ORDER BY p.username`, c.UserID)
		if err == nil {
			defer rows2.Close()
			for rows2.Next() {
				var s shareInfo
				if rows2.Scan(&s.Username, &s.CIDR) == nil {
					sharesToMe = append(sharesToMe, s)
				}
			}
		}
	}
	// (3) myMeshMembers: every other user in any active
	// mesh I belong to (and their /24). The query is
	// symmetric in mesh_id, so we deduplicate by username
	// server-side via the (mesh_id, user_id) PK.
	rows3, err := a.DB.Query(`
		SELECT p.username, COALESCE(s.cidr, '')
		  FROM mesh_members mm_self
		  JOIN mesh_members mm_other ON mm_other.mesh_id = mm_self.mesh_id
		  JOIN portal_users p ON p.id = mm_other.user_id
		  LEFT JOIN user_subnets s ON s.user_id = p.id AND s.status != 'disabled'
		 WHERE mm_self.user_id = ? AND p.id != ?
		   AND EXISTS (SELECT 1 FROM meshes m WHERE m.id = mm_self.mesh_id AND m.status = 'active')
		 ORDER BY p.username`, c.UserID, c.UserID)
	if err == nil {
		defer rows3.Close()
		seen := map[string]bool{}
		for rows3.Next() {
			var s shareInfo
			if rows3.Scan(&s.Username, &s.CIDR) == nil {
				if !seen[s.Username] {
					seen[s.Username] = true
					myMeshMembers = append(myMeshMembers, s)
				}
			}
		}
	}
	// (4) meshCount: how many active meshes I'm in.
	meshCount := 0
	_ = a.DB.QueryRow(`
		SELECT COUNT(DISTINCT mm.mesh_id)
		  FROM mesh_members mm
		  JOIN meshes m ON m.id = mm.mesh_id
		 WHERE mm.user_id = ? AND m.status = 'active'`, c.UserID).Scan(&meshCount)

	a.renderWithLayout(w, r, "user/devices.html", c, map[string]any{
		"MyNodes":        myNodesList,
		"PublicNodes":    publicNodes,
		"HasMyNodes":     len(myNodesList) > 0,
		"SubnetCIDR":     subnetCIDR,
		"SubnetStatus":   subnetStatus,
		"MySharesTo":     mySharesTo,
		"SharesToMe":     sharesToMe,
		"MyMeshMembers":  myMeshMembers,
		"MeshCount":      meshCount,
	})
}
