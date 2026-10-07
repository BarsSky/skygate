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
	hsUserID, username, _ = db.GetUserHSByID(s.dbc(), c.UserID)

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
	_ = s.dbc().QueryRow(
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
		// IsTaggedGhost is true when the node is owned by the
		// synthetic "tagged-devices" headscale user (id=2147455555)
		// instead of a real portal user. These nodes are the
		// pre-auth ghost registrations (install-time preauth keys
		// without --user) — headscale 0.29.2 has no `nodes move`
		// CLI, so the only recovery is B-mod-reregister: delete +
		// issue a fresh key bound to the current user. The
		// /my/devices template uses this flag to render the
		// "Re-register" button (only visible when IsTaggedGhost).
		IsTaggedGhost bool
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
		// ExpiryHint is a sub-classification that fires
		// only when ExpiryWarning == "expired" — it
		// distinguishes the three kinds of "expired"
		// state the operator can observe on a node:
		//
		//   "no_activity"  — LastSeen is empty /
		//                    unparseable. The device was
		//                    either force-expired by the
		//                    admin, force-removed from
		//                    headscale, or this row is a
		//                    snapshot-only entry that
		//                    never reconnected. Renew will
		//                    likely 410 Gone — the operator
		//                    should consider deleting the
		//                    row.
		//   "near_expiry"  — |LastSeen − Expiry| ≤ 5 min.
		//                    The device was online at or
		//                    near the moment the expiry
		//                    was set. Most likely cause:
		//                    the user ran `tailscale logout`
		//                    (which sets node.expiry=now
		//                    on the headscale side). The
		//                    device can re-register by
		//                    re-running `tailscale up` with
		//                    a fresh preauth key.
		//   "while_offline"— |LastSeen − Expiry| > 5 min
		//                    AND LastSeen is older than
		//                    Expiry. The device was offline
		//                    for the full TTL gap, so the
		//                    key expired naturally. This
		//                    is also the signature of an
		//                    admin force-expire (`headscale
		//                    nodes expire -i N --immediate`)
		//                    on a long-idle device.
		//
		// Empty for all non-expired rows. The /my/devices
		// template renders the hint as a small muted
		// caption directly under the red "expired" pill
		// (B170, v1.5.2 — operator 2026-08-25
		// "когда устройство разлогинилось из аккаунта
		// headscale то в таблице my devices отобразилась
		// информация что истек срок действия а не
		// устройство разлогинилось или что то еще, есть
		// ли возможность различить или истечение срока и
		// разлогин имеют одну механику?").
		// 2026-08-25.
		ExpiryHint string
		// LastSeenTime is the parsed RFC3339Nano
		// timestamp from headscale NodeView.LastSeen.
		// Zero when LastSeen is empty or unparseable.
		// Kept as a separate field (not just inlined
		// into the ExpiryHint computation) so future
		// B-checks / template additions can reuse the
		// parsed value without re-parsing the raw
		// string. 2026-08-25.
		LastSeenTime time.Time
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
	devicePrefs, _ := db.ListDeviceExitNodePrefsForUser(s.dbc(), c.UserID)
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
	// 2026-08-26: v1.5.2 (B188) — also cache the canonical
	// headscale tag per hostname. The /my/devices +
	// /admin/devices dropdown templates read this to
	// avoid synthesising the legacy `tag:exit-<host>`
	// form inline.
	tagByHost := map[string]string{}
	deviceMeta, _ := db.ListNodeOwnersByUsername(s.dbc(), username)
	for _, dn := range deviceMeta {
		if dn.Hostname != "" {
			osByHost[strings.ToLower(dn.Hostname)] = dn.OS
			typeByHost[strings.ToLower(dn.Hostname)] = dn.DeviceType
			tagByHost[strings.ToLower(dn.Hostname)] = dn.Tag
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
		// B287 (2026-09-22): a node wearing its per-device tag belongs to that
		// user NO MATTER what headscale calls the owner — headscale reassigns
		// every tagged node to the synthetic `tagged-devices` user, so the
		// `n.UserName == username` test alone rendered /my/devices EMPTY while
		// /admin/devices listed the user's own devices (as `tagged-devices`).
		// The tag `tag:dev-<user>-<host>` is the ownership record: it is minted
		// per user and headscale only accepts it once that user owns the node.
		if hsUserID.Valid && username != "" &&
			(n.UserName == username || db.HasPerDeviceTag(n.Tags, username, n.Hostname)) {
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
				// B176: headscale 0.29 requires tags to be
				// lowercase. The dev-tag displayed in the
				// UI must match what headscale actually
				// stores — otherwise the "applied" check
				// below would never find the tag (because
				// headscale has `tag:dev-skyadmin-skybars`
				// but the UI computes `tag:dev-skyadmin-
				// SkyBars` from n.Hostname). Pre-B176 this
				// caused node 35 "SkyBars" to show "⏳
				// pending" forever (see live-verify report
				// on 2026-08-25).
				devTag = fmt.Sprintf("tag:dev-%s-%s", username, strings.ToLower(n.Hostname))
				devTagApplied = hasTag(n.Tags, devTag)
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
				// B287: a node in headscale's synthetic `tagged-devices` user is
				// only a "ghost" (needing the B-mod-reregister flow) when NO
				// skygate per-device tag attributes it to this portal user. The
				// sentinel owner means either "registered with a key that had no
				// --user" (a real ghost) OR "wears a tag" (correctly attributed);
				// the tag is the ownership record, so a tagged device must not be
				// invited to re-register.
				IsTaggedGhost:        n.UserName == "tagged-devices" && !devTagApplied,
				IsSubnetRouter:       hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:           n.IsExitNode,
				MeshSubnet:           subnetCIDR,
				IsShared:             n.IsPublicView() || n.IsExitNode,
				DevTag:               devTag,
				DevTagApplied:        devTagApplied,
				HostnameLower:        strings.ToLower(n.Hostname),
				DeviceExitPref:       devicePrefByHost[strings.ToLower(n.Hostname)],
				DeviceExitViaEnabled: deviceViaEnabledByHost[strings.ToLower(n.Hostname)],
				OS:                   osByHost[strings.ToLower(n.Hostname)],
				DeviceType:           typeByHost[strings.ToLower(n.Hostname)],
				Expiry:               n.Expiry,
			})
		}
	}
	if username != "" {
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.ListNodeOwnerNodeIDsByUsername.
		snapIDList, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), username)
		// B287: the snapshot row's `username` can be headscale's synthetic
		// `tagged-devices` (it reassigns tagged nodes), while its TAG still
		// names the real owner — so a username lookup alone misses exactly the
		// devices this page exists to show. Union both views.
		if byTag, tagErr := db.ListNodeOwnerNodeIDsByUserTag(s.dbc(), username); tagErr == nil {
			snapIDList = append(snapIDList, byTag...)
		}
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
				// B176: see the same comment in the
				// live branch above — headscale 0.29
				// requires lowercase tags.
				devTag = fmt.Sprintf("tag:dev-%s-%s", username, strings.ToLower(n.Hostname))
				devTagApplied = hasTag(n.Tags, devTag)
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
				// B287: same rule as the live loop above — the sentinel owner
				// alone is not a ghost when a per-device tag names this user.
				IsTaggedGhost:        n.UserName == "tagged-devices" && !devTagApplied,
				IsSubnetRouter:       hasTag(n.Tags, "tag:subnet-router"),
				IsExitNode:           n.IsExitNode,
				MeshSubnet:           subnetCIDR,
				IsShared:             n.IsPublicView() || n.IsExitNode,
				DevTag:               devTag,
				DevTagApplied:        devTagApplied,
				HostnameLower:        strings.ToLower(n.Hostname),
				DeviceExitPref:       devicePrefByHost[strings.ToLower(n.Hostname)],
				DeviceExitViaEnabled: deviceViaEnabledByHost[strings.ToLower(n.Hostname)],
				OS:                   osByHost[strings.ToLower(n.Hostname)],
				DeviceType:           typeByHost[strings.ToLower(n.Hostname)],
				Expiry:               n.Expiry,
			})
		}
	}

	publicNodes := []headscale.NodeView{}
	for _, n := range all {
		if n.IsExitNode || n.IsPublicView() {
			// B188: stamp the canonical headscale tag
			// (from node_owner_map) so the dropdown
			// template can render the real value. The
			// headscale 0.29.x API does not return
			// forced_tags/valid_tags in our version,
			// so we have to look it up from skygate's
			// own table.
			n.DevTag = tagByHost[strings.ToLower(n.Hostname)]
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
		// B170 (v1.5.2) — when the row is expired,
		// also classify the cause so the operator can
		// tell "TTL ran out while offline" from
		// "device just logged out" from "force-expired
		// or stale snapshot". The signal is |LastSeen
		// − Expiry|:
		//   - LastSeen empty / unparseable → "no_activity"
		//   - |delta| ≤ 5 min (device online at or near
		//     the moment expiry was set) → "near_expiry"
		//   - otherwise (device was offline before the
		//     expiry was set) → "while_offline"
		// The 5-min window is a deliberate trade-off:
		// a `tailscale logout` from an active client
		// pushes node.expiry=now, so |delta| is the gap
		// between the last successful ping and the
		// logout moment — typically seconds, occasionally
		// minutes if the client had been idle. 5 min is
		// wide enough to catch slow clients and narrow
		// enough to NOT misclassify a natural TTL
		// expiry (which has a |delta| of ~30 days).
		if row.ExpiryWarning == "expired" {
			row.LastSeenTime, row.ExpiryHint = parseLastSeenAndClassify(t, row.LastSeen, now)
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
		_ = db.UpdateDeviceMetaAutoDetect(s.dbc(), mr.ID, detectedOS, detectedType)
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
		rows, err := s.dbc().Query(`
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
		rows2, err := s.dbc().Query(`
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
	rows3, err := s.dbc().Query(`
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
	_ = s.dbc().QueryRow(`
		SELECT COUNT(DISTINCT mm.mesh_id)
		  FROM mesh_members mm
		  JOIN meshes m ON m.id = mm.mesh_id
		 WHERE mm.user_id = $1 AND m.status = 'active'`, c.UserID).Scan(&meshCount)

	// B-mod-reregister (2026-09-13): count ghost nodes for
	// the banner. Inlined (not a separate helper) because the
	// earlier countTaggedGhosts() helper triggered a Go parser
	// edge case in devices.go — the inline loop is fine.
	taggedGhostCount := 0
	for _, r := range myNodesList {
		if r.IsTaggedGhost {
			taggedGhostCount++
		}
	}

	s.Backend.RenderWithLayout(w, r, "user/devices.html", c, map[string]any{
		"MyNodes":          myNodesList,
		"PublicNodes":      publicNodes,
		"HasMyNodes":       len(myNodesList) > 0,
		"TaggedGhostCount": taggedGhostCount,
		"SubnetCIDR":       subnetCIDR,
		"SubnetStatus":     subnetStatus,
		"MySharesTo":       mySharesTo,
		"SharesToMe":       sharesToMe,
		"MyMeshMembers":    myMeshMembers,
		"MeshCount":        meshCount,
		// v0.28.4: per-device exit-node prefs.
		"DeviceExitPrefs":    devicePrefByHost,
		"AvailableExitNodes": publicNodes, // for the per-device dropdown
		"FlashSuccess":       r.URL.Query().Get("ok"),
		"FlashError":         r.URL.Query().Get("err"),
		// B160 (v1.5.0): post-renew flash. The
		// PostMyDeviceRenew handler redirects to
		// /my/devices?renewed=<host> with the
		// just-renewed hostname so the template can
		// render "Device <host> session extended to
		// <new_expiry>". The hostname is the only
		// non-secret in the URL (the audit log
		// captures the full node ID + new expiry).
		"RenewedHost":      r.URL.Query().Get("renewed"),
		"RenewedNewExpiry": r.URL.Query().Get("new_expiry"),
		// B162 (v1.5.1): post-delete flash. The
		// PostMyDeviceDelete handler redirects to
		// /my/devices?deleted=<host> (URL-escaped
		// so hostnames with non-ASCII survive);
		// we render the success alert with the
		// hostname. The full node ID is in the
		// audit log (not in the URL).
		"DeletedHost": r.URL.Query().Get("deleted"),
		// B171 (v1.5.2): post-delete flash
		// extensions. The handler redirects to
		// /my/devices?deleted=<host>&deleted_rules=N
		// (&acl_err=...) so the user can see the
		// comprehensive-cleanup outcome (rules
		// removed count + optional ACL regen
		// error). The query string is parsed as
		// an int (rules) and a string (err). The
		// template renders the rules count via
		// .DeletedRules (the raw string, for
		// truthy checks) and .DeletedRulesCount
		// (the int, for `gt` comparisons). The
		// ACL error renders via .DeletedACLErr.
		"DeletedRules":      r.URL.Query().Get("deleted_rules"),
		"DeletedRulesCount": parseIntQuery(r.URL.Query().Get("deleted_rules")),
		"DeletedACLErr":     r.URL.Query().Get("acl_err"),
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
		"LastRefreshedAt":  time.Now().Unix(),
		"RefreshRequested": refreshRequested,
	})
}
