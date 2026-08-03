// Package my — devices.go owns GET /my/devices: list the
// current user's devices plus public/exit nodes. Performs
// a lazy-backfill of node_owner_map from headscale's
// preAuthKey history on every load so the user sees their
// tagged devices immediately.
//
// refactor-v0.30 Phase B step 5b (2026-07-29): moved
// from internal/handlers/handlers_my_devices.go. The
// handler used to be a method on *App; it now lives on
// *Service. The backfillNodeOwnership helper is a local
// copy — the canonical version lives in
// internal/handlers/handlers_node_ownership.go (where
// it's used by the /admin/devices page and the
// /my/devices backfill + the per-device tag auto-apply).
// The two copies are kept in sync; dedup is left as a
// future refactor (the function is ~250 lines and
// moving it to internal/nodeownership/ would touch every
// admin and per-device-handler file).
package my

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/devicemeta"
	"skygate/internal/headscale"
)

// GetMyDevices lists the current user's own devices
// plus the tailnet's public/exit nodes. Performs a lazy
// backfill of node_owner_map from headscale's preAuthKey
// history so the user sees their tagged devices on the
// first /my/devices load.
func (s *Service) GetMyDevices(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	var hsUserID sql.NullInt64
	var username string
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserHSByID
	hsUserID, username, _ = db.GetUserHSByID(s.DB, c.UserID)

	// 2026-07-21: v0.22.3 — read the user's subnet row
	// (denormalized on portal_users) so the /my/devices page
	// can show "Your personal subnet: 10.0.<uid>.0/24 (active)"
	// without an extra JOIN. Backfill above may have just
	// flipped the status to active (the SyncStatus call in
	// backfillNodeOwnership), so the value here is fresh.
	// v0.25.0 — read it HERE (not later) so we can fill
	// the new "Mesh subnet" column in the device rows.
	var subnetCIDR, subnetStatus string
	_ = s.DB.QueryRow(
		`SELECT subnet_cidr, subnet_status FROM portal_users WHERE id = $1`, c.UserID,
	).Scan(&subnetCIDR, &subnetStatus)

	// Get all nodes (cached). Reuse them for both my-nodes (filter by user)
	// and public nodes (filter by tag/exit) - one HTTP call to headscale
	// instead of two.
	// 2026-07-15: v0.12.0 — route to the user's own control plane.
	// The device list reflects the user's tailnet, not the
	// operator's primary one.
	t0 := time.Now()
	all, _ := s.Backend.HSForUserFn(c.UserID).ListAllNodes()

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
	//
	// The actual backfill is a callback (BackfillNodeOwnership
	// on the Service, set by main.go) — the implementation
	// lives in internal/handlers/handlers_node_ownership.go
	// and is shared with the /admin/devices page. Inlining
	// it here would mean keeping two ~250-line copies in
	// sync; a future refactor can move it to a shared
	// internal/nodeownership/ package.
	if c.UserID != 0 && s.BackfillNodeOwnership != nil {
		s.BackfillNodeOwnership(s.DB, all, c.UserID, username)
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
		// (e.g. "10.0.1.0/24 (admin)"). Empty for
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
		// DevTag is the per-device ACL tag
		// ("tag:dev-<user>-<hostname>") that the v0.28.0
		// per-device rules use as src in the headscale
		// ACL. Empty when the node has no hostname (rare;
		// headscale still uses the IP-based fallback for
		// such nodes). The /my/devices page surfaces it
		// so the user can verify the auto-applied tag is
		// present. 2026-07-24.
		DevTag string
		// DevTagApplied is true when DevTag appears in
		// the node's headscale tag list. The template
		// shows a green pill when applied, yellow when
		// pending (the next /my/devices load retries the
		// auto-apply). 2026-07-24.
		DevTagApplied bool
		// HostnameLower is the lowercased hostname, used
		// by the v0.28.4 template to look up the
		// per-device exit-node pref (the pref table is
		// keyed on lowercased hostname to match the
		// v0.28.0 tag:dev-<user>-<device> convention).
		// 2026-07-25: v0.28.4.
		HostnameLower string
		// DeviceExitPref is the device's preferred exit-node
		// tag (e.g. "tag:exit-relay-3"), or "" if no
		// per-device pref is set. Pre-resolved by the
		// handler from the devicePrefByHost map so the
		// template doesn't need a custom func. 2026-07-25:
		// v0.28.4.
		DeviceExitPref string
		// DeviceExitViaEnabled mirrors the via_enabled
		// flag from device_exit_node_prefs. When true,
		// the per-device grant is emitted with via=[].
		// When false (the safe default for Android), the
		// per-device grant is skipped. 2026-07-25:
		// v0.28.5.
		DeviceExitViaEnabled bool
		// OS is the device's operating system marker
		// ("windows" / "android" / "linux" / ...), shown
		// in /my/devices next to the hostname so the
		// operator can debug at a glance
		// ("base = Windows, check the Tailscale client
		// version"). Sourced from the auto-detect on the
		// first /my/devices load and editable by the
		// admin via /admin/devices/{id}/meta. 2026-07-29.
		OS string
		// DeviceType is "client" / "exit-node" / "subnet-router"
		// / "phone" — same origin as OS. 2026-07-29.
		DeviceType string
	}
	mySet := map[string]bool{}
	var myNodesList []myNodeRow

	// 2026-07-25: v0.28.4 — per-device preferred exit-node
	// map. Keyed by lowercased hostname. The /my/devices
	// template uses this to render the "Currently pinned"
	// badge per device and the set/clear form. Pre-fetched
	// here (not lazily) so the per-row myNodeRow builder
	// can look it up in O(1).
	devicePrefByHost := map[string]string{}
	deviceViaEnabledByHost := map[string]bool{}
	devicePrefs, _ := db.ListDeviceExitNodePrefsForUser(s.DB, c.UserID)
	for _, dp := range devicePrefs {
		if dp.DeviceHostname != "" {
			devicePrefByHost[strings.ToLower(dp.DeviceHostname)] = dp.ExitNodeTag
			deviceViaEnabledByHost[strings.ToLower(dp.DeviceHostname)] = dp.ViaEnabled
		}
	}
	// 2026-07-29: per-device OS + device_type prefetch.
	// Keyed on lowercased hostname (same convention as
	// the per-device exit-node pref above). The
	// auto-detect pass below populates the rows that
	// are still 'unknown' on the first /my/devices load.
	osByHost := map[string]string{}
	typeByHost := map[string]string{}
	deviceMeta, _ := db.ListNodeOwnersByUsername(s.DB, username)
	for _, dn := range deviceMeta {
		if dn.Hostname != "" {
			osByHost[strings.ToLower(dn.Hostname)] = dn.OS
			typeByHost[strings.ToLower(dn.Hostname)] = dn.DeviceType
		}
	}
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
			// 2026-07-24: v0.28.0 — build the per-device
			// ACL tag and check whether headscale has
			// actually applied it (the backfill above
			// issues the AddTag call; on a fresh deploy
			// the first /my/devices load may not have
			// landed yet, so the user might briefly see
			// "pending").
			devTag := ""
			devTagApplied := false
			if username != "" && n.Hostname != "" {
				devTag = fmt.Sprintf("tag:dev-%s-%s", username, n.Hostname)
				devTagApplied = hasTag(n.Tags, devTag)
			}
			myNodesList = append(myNodesList, myNodeRow{
				ID: n.ID, Hostname: n.Hostname, IP: ip,
				Online: n.Online, LastSeen: n.LastSeen,
				UserName:           n.UserName,
				IsPublic:           n.IsPublicView(),
				Source:             "live",
				Tags:               n.Tags,
				AvailableRoutes:    n.AvailableRoutes,
				ApprovedRoutes:     n.ApprovedRoutes,
				IsSubnetRouter:     hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:         n.IsExitNode,
				MeshSubnet:         subnetCIDR,
				IsShared:           n.IsPublicView() || n.IsExitNode,
				DevTag:             devTag,
				DevTagApplied:      devTagApplied,
				HostnameLower:      strings.ToLower(n.Hostname),
				DeviceExitPref:     devicePrefByHost[strings.ToLower(n.Hostname)],
				DeviceExitViaEnabled: deviceViaEnabledByHost[strings.ToLower(n.Hostname)],
				OS:                 osByHost[strings.ToLower(n.Hostname)],
				DeviceType:         typeByHost[strings.ToLower(n.Hostname)],
			})
		}
	}
	if username != "" {
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.ListNodeOwnerNodeIDsByUsername.
		snapIDList, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, username)
		// Build a set for O(1) membership test. The list is small
		// (a user's owned devices) but a map keeps the lookups in the
		// inner loop tidy.
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
			// 2026-07-24: v0.28.0 — same devTag
			// computation as the live branch. Snapshot
			// rows cover nodes that headscale has
			// reassigned to "tagged-devices" because of
			// tag:private; the per-device tag was
			// applied at snapshot time (see
			// backfillNodeOwnership → AddTag) so the
			// applied flag is the source of truth.
			devTag := ""
			devTagApplied := false
			if username != "" && n.Hostname != "" {
				devTag = fmt.Sprintf("tag:dev-%s-%s", username, n.Hostname)
				devTagApplied = hasTag(n.Tags, devTag)
			}
			myNodesList = append(myNodesList, myNodeRow{
				ID: n.ID, Hostname: n.Hostname, IP: ip,
				Online: n.Online, LastSeen: n.LastSeen,
				UserName:           n.UserName,
				IsPublic:           n.IsPublicView(),
				Source:             "snapshot",
				Tags:               n.Tags,
				AvailableRoutes:    n.AvailableRoutes,
				ApprovedRoutes:     n.ApprovedRoutes,
				IsSubnetRouter:     hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:         n.IsExitNode,
				MeshSubnet:         subnetCIDR,
				IsShared:           n.IsPublicView() || n.IsExitNode,
				DevTag:             devTag,
				DevTagApplied:      devTagApplied,
				HostnameLower:      strings.ToLower(n.Hostname),
				DeviceExitPref:     devicePrefByHost[strings.ToLower(n.Hostname)],
				DeviceExitViaEnabled: deviceViaEnabledByHost[strings.ToLower(n.Hostname)],
				OS:                 osByHost[strings.ToLower(n.Hostname)],
				DeviceType:         typeByHost[strings.ToLower(n.Hostname)],
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

	// 2026-07-29: per-device OS + device_type
	// auto-detect. Runs once per /my/devices load.
	// For every node in myNodesList whose os OR
	// device_type is still 'unknown' / '', we run
	// devicemeta.Detect with the headscale tag + route
	// hints and persist via
	// db.UpdateDeviceMetaAutoDetect (which only writes
	// if BOTH columns are still in the default state,
	// so an admin-set value is never clobbered).
	//
	// The in-memory `n.OS` / `n.DeviceType` for the
	// current response uses the freshly-detected value
	// even if the DB write was a no-op (the row was
	// already up to date).
	for _, mr := range myNodesList {
		if mr.OS != "" && mr.OS != devicemeta.OSUnknown {
			continue
		}
		if mr.DeviceType != "" && mr.DeviceType != devicemeta.TypeUnknown {
			continue
		}
		// Find the matching headscale node for tag +
		// route hints.
		var node headscale.NodeView
		var found bool
		for _, n := range all {
			if n.ID == mr.ID {
				node = n
				found = true
				break
			}
		}
		if !found {
			continue
		}
		detectedOS := devicemeta.DetectOS(mr.HostnameLower)
		detectedType := devicemeta.DetectType(node.Tags, node.ApprovedRoutes, node.AvailableRoutes, detectedOS)
		// Persist (no-op if the admin has manually set
		// the row). Errors are non-fatal — the next
		// /my/devices load will retry.
		_ = db.UpdateDeviceMetaAutoDetect(s.DB, mr.ID, detectedOS, detectedType)
		// Always update the in-memory view, so the
		// current page reflects the detected value
		// even if the DB write was a no-op.
		mr.OS = detectedOS
		mr.DeviceType = detectedType
	}

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
		rows, err := s.DB.Query(`
			SELECT p.username, s.cidr
			  FROM user_subnet_shares sh
			  JOIN user_subnets s ON s.user_id = sh.grantor_user_id
			  JOIN portal_users p ON p.id = sh.grantee_user_id
			 WHERE sh.grantor_user_id = $1 AND s.status != 'disabled'
			 ORDER BY p.username`, c.UserID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var si shareInfo
				if rows.Scan(&si.Username, &si.CIDR) == nil {
					mySharesTo = append(mySharesTo, si)
				}
			}
		}
		// (2) sharesToMe: someone (grantor) shared with me (grantee).
		rows2, err := s.DB.Query(`
			SELECT p.username, s.cidr
			  FROM user_subnet_shares sh
			  JOIN user_subnets s ON s.user_id = sh.grantor_user_id
			  JOIN portal_users p ON p.id = sh.grantor_user_id
			 WHERE sh.grantee_user_id = $1 AND s.status != 'disabled'
			 ORDER BY p.username`, c.UserID)
		if err == nil {
			defer rows2.Close()
			for rows2.Next() {
				var si shareInfo
				if rows2.Scan(&si.Username, &si.CIDR) == nil {
					sharesToMe = append(sharesToMe, si)
				}
			}
		}
	}
	// (3) myMeshMembers: every other user in any active
	// mesh I belong to (and their /24). The query is
	// symmetric in mesh_id, so we deduplicate by username
	// server-side via the (mesh_id, user_id) PK.
	rows3, err := s.DB.Query(`
		SELECT p.username, COALESCE(s.cidr, '')
		  FROM mesh_members mm_self
		  JOIN mesh_members mm_other ON mm_other.mesh_id = mm_self.mesh_id
		  JOIN portal_users p ON p.id = mm_other.user_id
		  LEFT JOIN user_subnets s ON s.user_id = p.id AND s.status != 'disabled'
		 WHERE mm_self.user_id = $1 AND p.id != $2
		   AND EXISTS (SELECT 1 FROM meshes m WHERE m.id = mm_self.mesh_id AND m.status = 'active')
		 ORDER BY p.username`, c.UserID, c.UserID)
	if err == nil {
		defer rows3.Close()
		seen := map[string]bool{}
		for rows3.Next() {
			var si shareInfo
			if rows3.Scan(&si.Username, &si.CIDR) == nil {
				if !seen[si.Username] {
					seen[si.Username] = true
					myMeshMembers = append(myMeshMembers, si)
				}
			}
		}
	}
	// (4) meshCount: how many active meshes I'm in.
	meshCount := 0
	_ = s.DB.QueryRow(`
		SELECT COUNT(DISTINCT mm.mesh_id)
		  FROM mesh_members mm
		  JOIN meshes m ON m.id = mm.mesh_id
		 WHERE mm.user_id = $1 AND m.status = 'active'`, c.UserID).Scan(&meshCount)

	s.Backend.RenderWithLayout(w, r, "user/devices.html", c, map[string]any{
		"MyNodes":              myNodesList,
		"PublicNodes":          publicNodes,
		"HasMyNodes":           len(myNodesList) > 0,
		"SubnetCIDR":           subnetCIDR,
		"SubnetStatus":         subnetStatus,
		"MySharesTo":           mySharesTo,
		"SharesToMe":           sharesToMe,
		"MyMeshMembers":        myMeshMembers,
		"MeshCount":            meshCount,
		// v0.28.4: per-device exit-node prefs.
		"DeviceExitPrefs":      devicePrefByHost,
		"AvailableExitNodes":   publicNodes, // for the per-device dropdown
		"FlashSuccess":         r.URL.Query().Get("ok"),
		"FlashError":           r.URL.Query().Get("err"),
	})
}

// backfillNodeOwnership was a local copy of the helper in
// internal/handlers/handlers_node_ownership.go. The
// refactor-v0.30 Phase B step 5b move replaced it with
// the BackfillNodeOwnership callback on the Service
// (set in main.go), so the actual work now lives in
// handlers_node_ownership.go's *App.backfillNodeOwnership
// and is invoked via the callback. The ~250-line
// implementation stays there; feature/my/devices.go
// just calls the callback. A future refactor can move
// the canonical implementation to a shared
// internal/nodeownership/ package; tracked as a
// follow-up.
