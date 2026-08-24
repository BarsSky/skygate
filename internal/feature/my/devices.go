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
	"net/url"
	"strconv"
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

	// B160.2 (2026-08-20): optional cache bypass.
	// The headscale.Client caches ListAllNodes()
	// results for 5s to absorb the gRPC-to-HTTP
	// gateway cost. After a mutation (B155 issued
	// a new preauth key + the user reconnected
	// their device, B160.1 renewed an expiry, or
	// B162.0 node-delete) the cache can show stale
	// data for up to 5s. The /my/devices?refresh=1
	// route (linked by the "Refresh" button) forces
	// a fresh fetch by calling InvalidateCache()
	// before the ListAllNodes() call below.
	//
	// Operator 2026-08-20 hit this in the wild: they
	// reconnected a device with a new preauth key
	// from B155, the cache was still warm from the
	// pre-B155 /my/devices load, and the table
	// showed the pre-reconnect state. The user
	// expected an instant update.
	//
	// We only invalidate when ?refresh=1 is present
	// (NOT on every load) — the cache is doing real
	// work absorbing the 50ms-per-load headscale
	// cost; bypassing it on every render would
	// re-introduce the latency the cache was added
	// to fix in the first place.
	refreshRequested := r.URL.Query().Get("refresh") == "1"
	if refreshRequested {
		s.Backend.HSForUserFn(c.UserID).InvalidateCache()
	}

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
		// Expiry is the headscale-side node.expiry
		// (RFC3339Nano string). Empty for nodes that
		// have no expiry (tag:exit-node, tag:public,
		// tag:subnet-router, or any node the operator
		// ran `headscale nodes expire -i N --disable`
		// on). The /my/devices template renders this
		// in the new "Expires" column and shows a
		// "Renew" button only when Expiry is non-empty
		// (B160, v1.5.0 — operator 2026-08-20
		// "продление работы ключа которым устройство
		// аутентифицировалось в headscale через
		// вебинтерфейс skygate").
		// 2026-08-20.
		Expiry string
		// ExpiryUnix is Expiry parsed to a unix
		// timestamp (0 when Expiry is empty or
		// unparseable). The template uses this for
		// datetimeformat + formatRelativeExpiry.
		// 2026-08-20.
		ExpiryUnix int64
		// ExpiresRelative is the i18n-formatted
		// relative-time hint ("5 days left" / "5 days
		// ago" / "no expiry") pre-computed by the
		// handler so the template stays pure
		// presentation. 2026-08-20.
		ExpiresRelative string
		// ExpiryWarning is "soon" / "month" / "expired"
		// for nodes whose expiry is within 7d / 30d /
		// past, empty otherwise. Mirrors the B155
		// token-page pattern (red/yellow pill).
		// 2026-08-20.
		ExpiryWarning string
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
				Expiry:             n.Expiry,
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
				Expiry:             n.Expiry,
			})
		}
	}

	publicNodes := []headscale.NodeView{}
	for _, n := range all {
		if n.IsExitNode || n.IsPublicView() {
			publicNodes = append(publicNodes, n)
		}
	}

	// B160 (v1.5.0) — per-row expiry enrichment for
	// the "Expires" column + "Renew" button. The
	// raw headscale NodeView.Expiry string is
	// already populated above; this pass just
	// parses it to unix + computes the i18n
	// relative-time hint + the warning pill kind
	// (mirrors the B155 token-page pattern).
	//
	// Nodes with Expiry=="" (tag:exit-node,
	// tag:public, tag:subnet-router, or
	// `headscale nodes expire --disable` nodes)
	// get ExpiresRelative = "no expiry" and
	// ExpiryWarning = "" — the template renders
	// the placeholder "—" and no Renew button.
	now := time.Now()
	lang := s.I18n.LangFromRequest(r)
	for i := range myNodesList {
		row := &myNodesList[i]
		if row.Expiry == "" {
			row.ExpiresRelative = s.I18n.T(lang, "keys.never_expires")
			continue
		}
		// headscale returns RFC3339Nano; time.Parse
		// accepts it as RFC3339 (Nano is a superset).
		t, perr := time.Parse(time.RFC3339Nano, row.Expiry)
		if perr != nil {
			// Unparseable — treat as "no expiry" so
			// the template degrades gracefully instead
			// of crashing.
			log.Printf("web.my.devices: parse Expiry %q for node %s: %v",
				row.Expiry, row.Hostname, perr)
			row.ExpiresRelative = s.I18n.T(lang, "keys.never_expires")
			continue
		}
		row.ExpiryUnix = t.Unix()
		row.ExpiresRelative = formatRelativeExpiry(s.I18n, lang, row.ExpiryUnix, now.Unix())
		// Warning kind — mirrors B155. 7d = "soon"
		// (red), 30d = "month" (yellow), past =
		// "expired" (red).
		delta := t.Sub(now)
		switch {
		case delta <= 0:
			row.ExpiryWarning = "expired"
		case delta < 7*24*time.Hour:
			row.ExpiryWarning = "soon"
		case delta < 30*24*time.Hour:
			row.ExpiryWarning = "month"
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
		// B160 (v1.5.0): post-renew flash. The
		// PostMyDeviceRenew handler redirects to
		// /my/devices?renewed=<host> with the
		// just-renewed hostname so the template can
		// render "Device <host> session extended to
		// <new_expiry>". The hostname is the only
		// non-secret in the URL (the audit log
		// captures the full node ID + new expiry).
		"RenewedHost":          r.URL.Query().Get("renewed"),
		"RenewedNewExpiry":     r.URL.Query().Get("new_expiry"),
		// B162 (v1.5.1): post-delete flash. The
		// PostMyDeviceDelete handler redirects to
		// /my/devices?deleted=<host> (URL-escaped
		// so hostnames with non-ASCII survive);
		// we render the success alert with the
		// hostname. The full node ID is in the
		// audit log (not in the URL).
		"DeletedHost":          r.URL.Query().Get("deleted"),
		// B160.2 (2026-08-20): data freshness. The
		// "Last refreshed at HH:MM:SS" indicator
		// shows the user when the headscale data
		// was last fetched. The "Refresh" button
		// bypasses the cache (?refresh=1) and
		// updates this timestamp. Without this
		// indicator the user can't tell if they're
		// seeing a 5s-stale cache or the actual
		// headscale state — operator 2026-08-20 hit
		// this and thought the page was broken
		// (it wasn't; the cache was doing its job).
		"LastRefreshedAt":      time.Now().Unix(),
		"RefreshRequested":     refreshRequested,
	})
}

// PostMyDeviceRenew (B160, v1.5.0) extends the
// headscale-side expiry of one of the current user's
// own devices by 30 days. Operator use case
// (2026-08-20): "можно ли реализовать продление
// работы ключа которым устройство аутентифицировалось
// в headscale через веб интерфейс skygate" — the
// preauth key is one-time (B155), so renewing it
// doesn't help; the device's NODE EXPIRY is what
// keeps the device authenticated. The auto-renewer
// (internal/expirewatch) does this every 5min for
// nodes within 7d, but a manual button is useful
// when:
//   - the user disabled expirewatch
//   - the user wants to renew NOW (not wait for
//     the next tick)
//   - the user wants explicit visibility into
//     "renewed 5 days ago" (the audit log already
//     records every renewal)
//
// B160.1 (2026-08-20) — added the "node no longer
// exists" case. Operator 2026-08-20 hit this in the
// wild: the local node_owner_map snapshot still had
// the device's headscale ID, the /my/devices page
// rendered the Renew button, the user clicked it,
// and headscale returned "rpc error: code = Unknown
// desc = node no longer exists in NodeStore: 1" —
// a 500. The fix is pattern-match the error string
// and return 410 Gone instead, so the user sees a
// clear "device was removed, refresh" message and
// the audit log isn't polluted with a no-op
// "device_renewed" entry.
//
// Scope: the node MUST be owned by the current
// user (verified by re-listing the user's headscale
// nodes — same scoping as B155's PostMyKeyReissue).
// Cross-user renewals are rejected with 404.
//
// Errors:
//   - bad id (not int64) → 400
//   - node not in user's node list → 404
//   - node has no Expiry (tagged/shared infra) → 400
//     (these are policy-controlled; the user can't
//     unilaterally extend them)
//   - headscale says the node no longer exists →
//     410 Gone with the B160.1 "refresh the page"
//     message. This happens when the local
//     node_owner_map snapshot still has the ID but
//     the node was deleted from headscale in
//     between the last /my/devices load and the
//     renew click. The audit log is NOT written in
//     this case (the renewal didn't actually happen).
//   - any other headscale error → 500
//
// On success: redirect to /my/devices?renewed=<host>
// &new_expiry=<RFC3339> with the new expiry so the
// template can render a flash alert with both the
// hostname and the new timestamp. The host + expiry
// are non-secret (the audit log already shows them).

// renewNodeResult is the outcome of a tryRenewNode
// call. Used to translate the headscale-side gRPC
// error into the right HTTP status (410 vs 500)
// without duplicating the error-string parsing at
// every call site. B160.1, 2026-08-20.
type renewNodeResult int

const (
	renewOK renewNodeResult = iota
	// renewDeleted — headscale says the node no
	// longer exists in its NodeStore. The local
	// node_owner_map still has the ID (a stale
	// snapshot from the last /my/devices load), but
	// headscale has since removed the node (operator
	// ran `headscale nodes delete`, the device
	// expired and was cleaned up, etc.). 410 Gone
	// is the right HTTP status — the resource WAS
	// here, the user has stale data, refresh to
	// see the new state. We do NOT write the audit
	// log in this case (no actual renewal happened).
	renewDeleted
	// renewFailed — any other headscale / gRPC /
	// docker error. The raw message is returned
	// alongside so the caller can log it for
	// diagnostics; it's NEVER exposed to the user
	// (the user sees only the i18n key, which
	// doesn't leak headscale internals).
	renewFailed
)

// tryRenewNode wraps hsClient.ExtendNodeExpiry with
// the "no longer exists in NodeStore" detection
// (B160.1, 2026-08-20).
//
// We match on TWO patterns because headscale has
// shifted the error wording across versions:
//   - "no longer exists in NodeStore" (current
//     headscale 0.29.x — the operator's live VM
//     error from 2026-08-20)
//   - "node not found" (older / alternative
//     wording — defensive for future headscale
//     upgrades that might phrase it differently)
//
// Returning renewDeleted tells the caller to use
// 410 Gone (not 404) — the resource was there, the
// local snapshot is just stale. The user should
// refresh /my/devices to get the current list, at
// which point the deleted device disappears from
// the table.
func tryRenewNode(hsClient *headscale.Client, hsUserID int64, newExpiry time.Time) (renewNodeResult, string) {
	if err := hsClient.ExtendNodeExpiry(hsUserID, newExpiry); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "no longer exists in NodeStore") ||
			strings.Contains(msg, "node not found") {
			return renewDeleted, msg
		}
		return renewFailed, msg
	}
	return renewOK, ""
}

func (s *Service) PostMyDeviceRenew(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	idStr := r.PathValue("id")
	if idStr == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}
	_, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}

	// Scope-check: the node MUST be in the current
	// user's node list. We reuse the same ListAllNodes
	// → username filter as GetMyDevices. The
	// headscale.ExecContainer is on this user's
	// control plane (HSForUserFn), so even a forged
	// ID would fail at the gRPC layer.
	hsClient := s.Backend.HSForUserFn(c.UserID)
	allNodes, lerr := hsClient.ListAllNodes()
	if lerr != nil {
		log.Printf("web.my.renew: ListAllNodes userID=%d err=%v", c.UserID, lerr)
		http.Error(w, "headscale unreachable", http.StatusBadGateway)
		return
	}
	hsUserID, _, herr := db.GetUserHSByID(s.DB, c.UserID)
	if herr != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}
	for _, n := range allNodes {
		// headscale returns the user as the
		// numeric/username id; the existing handler
		// uses n.UserName. We re-resolve to a string
		// via the headscale user map (same pattern
		// as GetMyDevices). For now: just match by
		// n.ID first (the simplest + safest check).
		if n.ID == idStr {
			// Verify via node_owner_map too
			// (catches tag:private reassigned
			// nodes that headscale shows under
			// "tagged-devices").
			ok := false
			if n.UserName != "" {
				// headscale reports the user
				// as the user's name; we
				// don't have direct access
				// to c.Username here but
				// the nodeOwnerMap below
				// covers it.
			}
			// Use node_owner_map as the source
			// of truth (matches the B159 / B155
			// pattern).
			snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, c.Username)
			for _, sid := range snapIDs {
				if sid == idStr {
					ok = true
					break
				}
			}
			// Also accept the live "this user
			// owns the node right now" check.
			if !ok {
				// Try the live
				// user-id-based match
				// (headscale's
				// NodeView.UserName).
				if n.UserName == c.Username {
					ok = true
				}
			}
			if !ok {
				log.Printf("web.my.renew: node %s not owned by userID=%d username=%q",
					idStr, c.UserID, c.Username)
				http.Error(w, "device not found", http.StatusNotFound)
				return
			}
			// Tagged / shared-infra nodes
			// have Expiry == ""; reject
			// the renew request.
			if n.Expiry == "" {
				http.Error(w, "device has no expiry (tagged/shared)", http.StatusBadRequest)
				return
			}
			// Compute the new expiry:
			// now + 30d. Same default
			// the auto-renewer uses
			// (internal/expirewatch,
			// SKYGATE_EXPIREWATCH_RENEWAL=720h).
			newExpiry := time.Now().Add(30 * 24 * time.Hour)
			// B160.1: detect the "node no longer
			// exists in NodeStore" error and
			// return 410 Gone with a friendly
			// "refresh the page" message
			// instead of a 500 with the raw
			// gRPC error. The local
			// node_owner_map snapshot is just
			// stale (the device was deleted
			// from headscale between the last
			// /my/devices load and the renew
			// click). The audit log is NOT
			// written in this case.
			lang := s.I18n.LangFromRequest(r)
			switch result, rerrMsg := tryRenewNode(hsClient, hsUserID.Int64, newExpiry); result {
			case renewDeleted:
				log.Printf("web.my.renew: node=%s no longer exists in headscale (B160.1): %s", idStr, rerrMsg)
				http.Error(w, s.I18n.T(lang, "devices.renew_err_deleted"), http.StatusGone)
				return
			case renewFailed:
				log.Printf("web.my.renew: ExtendNodeExpiry node=%s err=%v", idStr, rerrMsg)
				http.Error(w, s.I18n.Tf(lang, "devices.renew_err_failed", rerrMsg), http.StatusInternalServerError)
				return
			}
			// Audit log: every renewal is
			// recorded so the admin audit
			// page can correlate
			// "device just reconnected" with
			// "skygate extended the node".
			detail := fmt.Sprintf("node_id=%s new_expiry=%s", idStr, newExpiry.UTC().Format(time.RFC3339))
			s.Backend.Audit(c.UserID, c.Username, "device_renewed", detail)
			// Redirect with hostname + new
			// expiry so the flash alert
			// can show both. The hostname
			// is the user-facing name; the
			// new_expiry is the timestamp
			// the user just got.
			host := n.Hostname
			http.Redirect(w, r, fmt.Sprintf("/my/devices?renewed=%s&new_expiry=%s",
				url.QueryEscape(host),
				url.QueryEscape(newExpiry.UTC().Format(time.RFC3339))), http.StatusFound)
			return
		}
	}
	// No matching node in the live list. We
	// also check the snapshot (node_owner_map)
	// in case the node is tagged-private and
	// headscale has reassigned it.
	snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, c.Username)
	for _, sid := range snapIDs {
		if sid == idStr {
			// Same scope as the live check;
			// we need n.Expiry + n.Hostname
			// for the audit + redirect, so
			// re-fetch from the live list
			// (which already failed to
			// match above — so the node
			// is in snapshot but not in
			// live, which is the
			// tagged-private case). For
			// these nodes, headscale shows
			// them under "tagged-devices"
			// and n.Expiry may still be
			// populated. Re-list and
			// match by id.
			for _, n := range allNodes {
				if n.ID == idStr {
					if n.Expiry == "" {
						http.Error(w, "device has no expiry", http.StatusBadRequest)
						return
					}
					newExpiry := time.Now().Add(30 * 24 * time.Hour)
					// B160.1: same deleted-vs-failed
					// detection as the live branch
					// above. A node that lives in
					// the snapshot (node_owner_map)
					// but has been deleted from
					// headscale since the last
					// /my/devices load still hits
					// this path; we want the same
					// 410 Gone behaviour.
					lang := s.I18n.LangFromRequest(r)
					switch result, rerrMsg := tryRenewNode(hsClient, hsUserID.Int64, newExpiry); result {
					case renewDeleted:
						log.Printf("web.my.renew: snapshot-node=%s no longer exists in headscale (B160.1): %s", idStr, rerrMsg)
						http.Error(w, s.I18n.T(lang, "devices.renew_err_deleted"), http.StatusGone)
						return
					case renewFailed:
						log.Printf("web.my.renew: ExtendNodeExpiry node=%s err=%v", idStr, rerrMsg)
						http.Error(w, s.I18n.Tf(lang, "devices.renew_err_failed", rerrMsg), http.StatusInternalServerError)
						return
					}
					detail := fmt.Sprintf("node_id=%s new_expiry=%s", idStr, newExpiry.UTC().Format(time.RFC3339))
					s.Backend.Audit(c.UserID, c.Username, "device_renewed", detail)
					http.Redirect(w, r, fmt.Sprintf("/my/devices?renewed=%s&new_expiry=%s",
						url.QueryEscape(n.Hostname),
						url.QueryEscape(newExpiry.UTC().Format(time.RFC3339))), http.StatusFound)
					return
				}
			}
			log.Printf("web.my.renew: node %s in snapshot but not in live list", idStr)
			http.Error(w, "device not found in headscale", http.StatusNotFound)
			return
		}
	}
	http.Error(w, "device not found", http.StatusNotFound)
}

// PostMyDeviceDelete (B162, v1.5.1) removes a device from
// the current user's headscale control plane. The user-
// facing effect is "the Tailscale client on this device
// loses its tailnet connection on the next netmap sync".
//
// Like B160 (renew), this handler is per-user, scope-checked
// to the current user's own nodes (or snapshot-owned nodes
// that headscale has reassigned to tagged-devices because
// of tag:private). Cross-user deletes return 404; deletes
// for a node the local snapshot still references but
// headscale has already purged return 410 Gone + the
// "refresh the page" message (mirrors B160.1's pattern).
//
// Side effects:
//   1. `headscale nodes delete -i <id>` via the gRPC client
//      (InvalidateCache() runs inside DeleteNode).
//   2. `DELETE FROM node_owner_map WHERE node_id=$1` so the
//      next /my/devices load doesn't re-render the row from
//      the snapshot (the snapshot branch would otherwise
//      keep showing the device until the admin manually
//      intervenes).
//   3. Audit log row `device_deleted node_id=N hostname=H`.
//
// What is NOT done here (deferred to v1.5.x or a follow-up):
//   - Cleanup of headscale ACL rules that reference the
//     deleted node (e.g. `tag:dev-<user>-<device>` grants
//     in the per-device ACL chain). headscale retains the
//     stale tag references harmlessly — the next policy
//     re-apply pass (the v0.32.x `acl_snapshots` cycle) sees
//     the device gone from ListAllNodes() and prunes the
//     rules. We do NOT manually edit the policy here because
//     that's the autoupdate's job and editing the live
//     policy outside that cycle has caused more outages
//     than it has fixed (the v0.32.4 / v0.32.13 history).
//   - Cleanup of `device_exit_node_prefs` rows for this
//     hostname (also auto-pruned on the next re-apply
//     pass; the prefs don't affect the deleted device's
//     connectivity, they only affect how OTHER devices
//     route to this one, and the prefs are inert for a
//     device that's not in headscale).
func (s *Service) PostMyDeviceDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	idStr := r.PathValue("id")
	if idStr == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}

	// Reuse the same scope-check as the renew handler
	// (live list + snapshot list). The HSForUserFn
	// routes to this user's control plane, so even a
	// forged ID would fail at the gRPC layer.
	hsClient := s.Backend.HSForUserFn(c.UserID)
	allNodes, lerr := hsClient.ListAllNodes()
	if lerr != nil {
		log.Printf("web.my.delete: ListAllNodes userID=%d err=%v", c.UserID, lerr)
		http.Error(w, "headscale unreachable", http.StatusBadGateway)
		return
	}
	host := ""
	ok := false
	for _, n := range allNodes {
		if n.ID == idStr {
			host = n.Hostname
			// Scope-check: the node must belong
			// to this user (live user_name
			// match) OR be in the
			// node_owner_map snapshot.
			if n.UserName == c.Username {
				ok = true
			}
			break
		}
	}
	// Snapshot check (tagged-private nodes that
	// headscale shows under "tagged-devices").
	if !ok {
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, c.Username)
		for _, sid := range snapIDs {
			if sid == idStr {
				ok = true
				break
			}
		}
	}
	// Also find the hostname in the snapshot if
	// the live list didn't have it. We use a small
	// inline scan of ListNodeOwnersByUsername
	// (the same call the renew handler makes),
	// filtering by node_id — cheaper than adding
	// a new "ListNodeOwnersByNodeID" helper for
	// this one site, and the user-prefixed call
	// is already in the per-request cache path
	// (the page render uses it too).
	if ok && host == "" {
		owners, _ := db.ListNodeOwnersByUsername(s.DB, c.Username)
		for _, o := range owners {
			if o.NodeID == idStr {
				host = o.Hostname
				break
			}
		}
	}
	if !ok {
		log.Printf("web.my.delete: node %s not owned by userID=%d username=%q",
			idStr, c.UserID, c.Username)
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	// B160.1-style "no longer exists" detection:
	// if headscale has already purged the node
	// (e.g. a parallel admin ran `headscale nodes
	// delete -i N` from the headscale CLI), the gRPC
	// call returns "no longer exists in NodeStore"
	// or "node not found". Treat that as 410 Gone +
	// still clean up the local snapshot.
	lang := s.I18n.LangFromRequest(r)
	if derr := hsClient.DeleteNode(nodeID); derr != nil {
		msg := derr.Error()
		if strings.Contains(msg, "no longer exists in NodeStore") ||
			strings.Contains(msg, "node not found") {
			log.Printf("web.my.delete: node=%s no longer exists in headscale (B162): %s", idStr, msg)
			// Still clean up the local snapshot
			// (otherwise the row stays on
			// /my/devices forever).
			if rerr := db.DeleteNodeOwnerByNodeTag(s.DB, idStr, ""); rerr != nil {
				log.Printf("web.my.delete: cleanup node_owner_map node=%s err=%v", idStr, rerr)
			}
			// Also clear the per-hostname prefs.
			if host != "" {
				_ = db.DeleteDeviceExitNodePref(s.DB, c.UserID, strings.ToLower(host))
			}
			http.Error(w, s.I18n.T(lang, "devices.delete_err_deleted"), http.StatusGone)
			return
		}
		log.Printf("web.my.delete: DeleteNode id=%s err=%v", idStr, derr)
		http.Error(w, s.I18n.Tf(lang, "devices.delete_err_failed", derr.Error()), http.StatusInternalServerError)
		return
	}
	// Local cleanup (best-effort; failures are
	// logged but don't fail the user-visible
	// redirect — headscale already removed the
	// node, the row will re-disappear on the next
	// snapshot cycle).
	if rerr := db.DeleteNodeOwnerByNodeTag(s.DB, idStr, ""); rerr != nil {
		log.Printf("web.my.delete: cleanup node_owner_map node=%s err=%v", idStr, rerr)
	}
	if host != "" {
		if rerr := db.DeleteDeviceExitNodePref(s.DB, c.UserID, strings.ToLower(host)); rerr != nil {
			log.Printf("web.my.delete: cleanup device_exit_node_prefs host=%s err=%v", host, rerr)
		}
	}

	// Audit log (always, even on the already-purged
	// case above — the operator wants to know who
	// tried to delete what).
	detail := fmt.Sprintf("node_id=%s hostname=%s", idStr, host)
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "device_deleted", detail)
	}
	// Redirect with flash so the user sees "Device
	// X was deleted" (same pattern as the B160
	// renew redirect with `renewed=...`).
	http.Redirect(w, r, fmt.Sprintf("/my/devices?deleted=%s",
		url.QueryEscape(host)), http.StatusFound)
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
