package admin

// devices.go — admin devices page (/admin/devices) + tag/untag/sync.
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/handlers_admin_nodes.go.
//
// Handlers: GetAdminDevices, PostAdminDevicesSyncFromHeadscale,
// PostAdminNodeTag, PostAdminNodeUntag. Helper: nodeTagRefusedForUserDevice
// (pure function, unit-tested in the original handlers).
//
// 2026-08-09: v0.33.1.20 — added PostAdminDevicesForceBackfillTags
// (per-user Backfill loop that runs against ALL portal users at
// once) and PostAdminDeviceTransfer (resolve orphan rows like the
// v0.33.1.19 svyatoslava conflict by reassigning a node to a
// different portal user). Together they form the "fix everything"
// admin toolkit: sync the DB from headscale, force re-apply the
// per-user dev-tags, transfer misattributed devices.

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/devicemeta"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/headscale"
	"skygate/internal/nodeownership"
)

// GetAdminDevices renders the /admin/devices page — all headscale
// nodes across the tailnet, with per-device ACL tags and the
// available-exit-node list for the per-device preferred-exit
// dropdown. Admin-only.
func (s *Service) GetAdminDevices(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	users, _ := s.HSGlobalFn().ListUsers()
	allNodes, _ := s.HSGlobalFn().ListAllNodes()

	devTags, _ := db.GetPerUserDeviceTags(s.DB, "")
	devTagMap := make(map[string]string, len(devTags))
	for _, t := range devTags {
		devTagMap[t.Hostname] = t.Tag
	}

	deviceExitPrefs, _ := db.ListAllDeviceExitNodePrefs(s.DB)
	deviceExitPrefMap := make(map[string]string, len(deviceExitPrefs))
	deviceExitViaMap := make(map[string]bool, len(deviceExitPrefs))
	skygateUserByName := make(map[string]int64, len(users))
	for _, u := range users {
		if u.Name != "" {
			skygateUserByName[u.Name] = 0
		}
	}
	for name := range skygateUserByName {
		var id int64
		if err := s.DB.QueryRow(
			`SELECT id FROM portal_users WHERE username = $1`, name,
		).Scan(&id); err == nil {
			skygateUserByName[name] = id
		}
	}
	skygateUserByHost := make(map[string]int64, len(devTags))
	for _, dt := range devTags {
		const prefix = "tag:dev-"
		if !strings.HasPrefix(dt.Tag, prefix) {
			continue
		}
		rest := strings.TrimPrefix(dt.Tag, prefix)
		idx := strings.LastIndex(rest, "-")
		if idx < 0 {
			continue
		}
		userName := rest[:idx]
		if uid, ok := skygateUserByName[userName]; ok && uid > 0 {
			skygateUserByHost[strings.ToLower(dt.Hostname)] = uid
		}
	}
	for _, dp := range deviceExitPrefs {
		if dp.DeviceHostname == "" {
			continue
		}
		key := strconv.FormatInt(dp.UserID, 10) + ":" + strings.ToLower(dp.DeviceHostname)
		deviceExitPrefMap[key] = dp.ExitNodeTag
		deviceExitViaMap[key] = dp.ViaEnabled
	}
	exits, _ := s.HSGlobalFn().ListExitNodes()

	userExitPrefs, _ := db.ListAllUserExitNodePrefs(s.DB)
	userExitPrefMap := make(map[string]string, len(userExitPrefs))
	for _, ep := range userExitPrefs {
		userExitPrefMap[strconv.FormatInt(ep.UserID, 10)] = ep.ExitNodeTag
	}

	// 2026-07-29: per-device OS + device_type lookup
	// by node_id (the row keyed on headscale node id).
	// We pre-fetch all rows once and index by node_id
	// so the inner loop is O(1). The auto-detect pass
	// itself runs in /my/devices; the admin page just
	// reads the result.
	osByNodeID := make(map[string]string)
	typeByNodeID := make(map[string]string)
	owners, _ := db.ListNodeOwnersByUsername(s.DB, "") // empty username = all rows
	for _, o := range owners {
		if o.NodeID != "" {
			osByNodeID[o.NodeID] = o.OS
			typeByNodeID[o.NodeID] = o.DeviceType
		}
	}

	// Build the view-model slice the template renders.
	// headscale.NodeView has no OS/DeviceType fields, so
	// we wrap each row with the metadata the /admin/devices
	// template needs. Same approach as feature/my/devices.go.
	type adminDeviceRow struct {
		headscale.NodeView
		OS         string
		DeviceType string
	}
	deviceRows := make([]adminDeviceRow, 0, len(allNodes))
	for _, n := range allNodes {
		os, typ := osByNodeID[n.ID], typeByNodeID[n.ID]
		// If the row is still 'unknown' / '', try the
		// auto-detect on the fly so the admin page
		// always shows a value (the /my/devices backfill
		// is the canonical write path, but the admin
		// shouldn't have to load /my/devices to see
		// icons on /admin/devices). The DB write is
		// best-effort — admin can re-run /my/devices to
		// persist.
		if (os == "" || os == devicemeta.OSUnknown) &&
			(typ == "" || typ == devicemeta.TypeUnknown) {
			detectedOS := devicemeta.DetectOS(n.Hostname)
			detectedType := devicemeta.DetectType(n.Tags, n.ApprovedRoutes, n.AvailableRoutes, detectedOS)
			_ = db.UpdateDeviceMetaAutoDetect(s.DB, n.ID, detectedOS, detectedType)
			os, typ = detectedOS, detectedType
		}
		deviceRows = append(deviceRows, adminDeviceRow{
			NodeView: n, OS: os, DeviceType: typ,
		})
	}

	// 2026-08-06: per-device "dead rules" count. For each
	// device, count rules in device_rules whose exit_node_id
	// doesn't match the device's preferred exit-node (or
	// whose preferred is unset). The admin template renders
	// a per-row warning with the count and a link to the
	// device's exit-rule subset.
	//
	// Build (hostname → preferred host) from the per-device +
	// per-user pref maps. Per-device wins; per-user is the
	// fallback for devices that have no override.
	hostnameToUserID := map[string]int64{}
	for hn, uid := range skygateUserByHost {
		hostnameToUserID[strings.ToLower(hn)] = uid
	}
	prefByHostname := map[string]string{}
	for _, dp := range deviceExitPrefs {
		hn := strings.ToLower(dp.DeviceHostname)
		if hn == "" {
			continue
		}
		prefByHostname[hn] = exit_rules.TagToHostname(dp.ExitNodeTag)
	}
	for hn, uid := range hostnameToUserID {
		if _, has := prefByHostname[hn]; has {
			continue
		}
		prefKey := strconv.FormatInt(uid, 10)
		if v, ok := userExitPrefMap[prefKey]; ok {
			prefByHostname[hn] = exit_rules.TagToHostname(v)
		}
	}
	// Walk all enabled device_rules and count dead rules per
	// hostname.
	allRules, _ := db.GetAllRulesForAdmin(s.DB)
	deadByHostname := map[string]int{}
	// Pre-bucket by hostname so the cross-check is O(N).
	for _, r := range allRules {
		hn := strings.ToLower(r.DeviceName)
		if hn == "" {
			continue
		}
		pref := prefByHostname[hn]
		if !exit_rules.IsRuleApplicable(r.ExitNodeID, pref) {
			deadByHostname[hn]++
		}
	}
	// Augment each row with the count for template rendering.
	type adminDeviceRow2 struct {
		adminDeviceRow
		DeadRuleCount int
	}
	deviceRowsWithDead := make([]adminDeviceRow2, 0, len(deviceRows))
	for _, dr := range deviceRows {
		deviceRowsWithDead = append(deviceRowsWithDead, adminDeviceRow2{
			adminDeviceRow: dr,
			DeadRuleCount:  deadByHostname[strings.ToLower(dr.Hostname)],
		})
	}

	s.Backend.RenderWithLayout(w, r, "admin/devices.html", c, map[string]any{
		"Nodes":             deviceRowsWithDead,
		"Users":             users,
		"FlashSuccess":      r.URL.Query().Get("ok"),
		"FlashError":        r.URL.Query().Get("err"),
		"DevTagMap":         devTagMap,
		"DeviceExitPrefMap": deviceExitPrefMap,
		"DeviceExitViaMap":  deviceExitViaMap,
		"AvailableExits":    exits,
		"UserExitPrefMap":   userExitPrefMap,
		"SkygateUserByName": skygateUserByName,
		"SkygateUserByHost": skygateUserByHost,
		// 2026-08-09: v0.33.1.20 — list of portal user
		// names for the per-row "Transfer" dropdown. The
		// handler PostAdminDeviceTransfer takes a portal
		// username (not a headscale user name) because the
		// transfer's effect is to rewrite node_owner_map
		// + call hs.AddTag/UntagNode (both keyed on the
		// portal-side state). We filter the dropdown to
		// only users with a non-zero portal id (i.e.
		// headscale users with a matching skygate account)
		// so the operator doesn't see "tagged-devices"
		// (the synthetic headscale user) as a target.
		"TransferTargets": transferTargets(skygateUserByName),
	})
}

// PostAdminDevicesSyncFromHeadscale is the v0.14.0 "Sync from
// headscale" button. INSERTs any missing rows in node_owner_map
// + UPDATEs drifted tags. Admin-only.
func (s *Service) PostAdminDevicesSyncFromHeadscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		http.Error(w, "headscale list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var syncInfos []db.SyncNodeInfo
	for _, n := range nodes {
		tag := ""
		for _, t := range n.Tags {
			if t == headscale.TagPublicTag || t == headscale.TagPrivateTag {
				continue
			}
			tag = t
			break
		}
		if tag == "" {
			for _, t := range n.Tags {
				if t != "" {
					tag = t
					break
				}
			}
		}
		var hsUID int64
		if n.UserID != "" {
			if v, perr := strconv.ParseInt(n.UserID, 10, 64); perr == nil {
				hsUID = v
			}
		}
		syncInfos = append(syncInfos, db.SyncNodeInfo{
			ID:       n.ID,
			Hostname: n.Hostname,
			Tag:      tag,
			Username: n.UserName,
			HSUserID: hsUID,
			TaggedBy: c.UserID,
		})
	}
	ins, upd, err := db.SyncNodesFromHeadscale(s.DB, syncInfos)
	if err != nil {
		http.Error(w, "sync failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "node_sync_from_headscale",
		fmt.Sprintf("inserted=%d updated=%d", ins, upd))
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/devices?ok=%s", url.QueryEscape(
			fmt.Sprintf("Sync from headscale: %d inserted, %d updated", ins, upd))), http.StatusSeeOther)
}

// PostAdminNodeTag adds a headscale tag to a node. The
// v0.30.1 guard (nodeTagRefusedForUserDevice) refuses exit-node
// tags on per-user devices. Admin-only.
func (s *Service) PostAdminNodeTag(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}

	var origUserID, origUserName string
	var nodeTags []string
	hs := s.HSGlobalFn()
	if nodes, err := hs.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				origUserID = n.UserID
				origUserName = n.UserName
				nodeTags = n.Tags
				break
			}
		}
	}

	if refused, msg, hadTag := nodeTagRefusedForUserDevice(nodeID, tag, nodeTags); refused {
		s.Backend.Audit(c.UserID, c.Username, "node_tag_refused",
			fmt.Sprintf("node=%d attempted_tag=%s reason=user_device node_had=%s",
				nodeID, tag, hadTag))
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	if err := hs.TagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if origUserID != "" && origUserName != "" {
		nodeIDStr := strconv.FormatInt(nodeID, 10)
		var hsUID int64
		if n, err := strconv.ParseInt(origUserID, 10, 64); err == nil {
			hsUID = n
		}
		if origUserName == "tagged-devices" {
			_ = db.UpdateNodeOwnerTag(s.DB, nodeIDStr, tag, c.UserID)
		} else {
			_ = db.UpsertNodeOwner(s.DB, nodeIDStr, hsUID, origUserName, tag, c.UserID)
		}
	}

	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "node_tag", fmt.Sprintf("node=%d tag=%s owner=%s", nodeID, tag, origUserName))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

// PostAdminNodeUntag removes a headscale tag from a node and
// cleans up the corresponding node_owner_map row. Admin-only.
func (s *Service) PostAdminNodeUntag(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}
	hs := s.HSGlobalFn()
	if err := hs.UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = db.DeleteNodeOwnerByNodeTag(s.DB, strconv.FormatInt(nodeID, 10), tag)

	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

// nodeTagRefusedForUserDevice is the v0.30.1 guard extracted as
// a pure function so it can be unit-tested without spinning up
// HTTP/headscale/dockerexec.
//
// Returns (true, msg, existingDevTag) when the request MUST be
// rejected: admin asked to add an exit-node-like tag to a node
// that already carries a per-user device tag (tag:dev-<user>-
// <device>). Returns (false, "", "") when the request is safe
// to apply.
//
// Why the guard exists: on 2026-07-28, user1's Windows box
// "base" (headscale id=7, tag:dev-user1-base) was found
// carrying tag:exit-node in headscale — set via direct headscale
// CLI, NOT through skygate (audit_log has no node=7 entries).
// The Tailscale Windows client on base then auto-selected
// "Base" as the exit-node (0ms self-loop = lowest metric), and
// all of base's internet traffic went to /dev/null. User
// reported: "пропал доступ в сеть" + "exit node не выбирается
// корректно".
//
// This guard prevents the SAME shape of mistake from being
// introduced through the skygate admin UI (the operator's
// most common accidental path: clicking "Tag as exit-node" on
// the wrong row in /admin/devices). It does NOT block direct
// headscale CLI manipulation — that path bypasses skygate
// entirely and is an operator-only decision.
func nodeTagRefusedForUserDevice(nodeID int64, requestedTag string, currentTags []string) (refused bool, message string, existingDevTag string) {
	if !strings.HasPrefix(requestedTag, "tag:exit") {
		return false, "", ""
	}
	for _, t := range currentTags {
		if strings.HasPrefix(t, "tag:dev-") {
			msg := fmt.Sprintf(
				"refuse: node %d already has per-user device tag %q — "+
					"cannot add exit-node tag %q. Per-user devices (tag:dev-*) "+
					"must never be exit-node candidates. To make this node a "+
					"relay, first untag the per-user tag (PostAdminNodeUntag).",
				nodeID, t, requestedTag)
			return true, msg, t
		}
	}
	return false, "", ""
}

// transferTargets returns the sorted list of portal usernames
// that have a non-zero entry in skygateUserByName (i.e. headscale
// users with a matching skygate account). Excludes the synthetic
// "tagged-devices" headscale user (which has no portal counterpart).
// Used by the /admin/devices per-row "Transfer" dropdown so the
// operator only sees valid transfer destinations.
//
// 2026-08-09: v0.33.1.20.
func transferTargets(skygateUserByName map[string]int64) []string {
	out := []string{}
	for name, id := range skygateUserByName {
		if id > 0 && name != "" && name != "tagged-devices" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PostAdminDeviceMeta is the v0.31.x manual-override form
// for the per-device OS + device_type markers. Admin-only.
//
// The auto-detect (internal/devicemeta.Detect) handles ~80%
// of hostnames correctly (DESKTOP-*, MSI*, iPhone, MacBook,
// raspberrypi, skygate-host-1, ...). The remaining 20% —
// Samsung model numbers like "A71", custom hostnames like
// "laptop", etc. — fall back to "unknown" and need an
// admin-set value. The auto-detect pass (which runs every
// /my/devices load) deliberately skips rows where either
// column has a non-default value, so the manual override
// is sticky across reloads.
//
// To re-enable the auto-detect for a node (e.g. after a
// hostname rename), the admin can set BOTH columns to
// "unknown" via this form — the next /my/devices load will
// re-run Detect() and persist the new guess.
//
// 2026-07-29.
func (s *Service) PostAdminDeviceMeta(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	idStr := r.FormValue("node_id")
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}
	os := strings.TrimSpace(r.FormValue("os"))
	typeIn := strings.TrimSpace(r.FormValue("device_type"))
	if !devicemeta.IsOSValid(os) {
		http.Error(w, "invalid os: "+os, http.StatusBadRequest)
		return
	}
	if !devicemeta.IsTypeValid(typeIn) {
		http.Error(w, "invalid device_type: "+typeIn, http.StatusBadRequest)
		return
	}
	// Default the empty form value to "unknown" so the
	// auto-detect re-runs on the next /my/devices load.
	if os == "" {
		os = devicemeta.OSUnknown
	}
	if typeIn == "" {
		typeIn = devicemeta.TypeUnknown
	}
	if err := db.SetDeviceMetaNodeOwner(s.DB, strconv.FormatInt(nodeID, 10), os, typeIn); err != nil {
		http.Error(w, "db write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "device_meta_set",
		fmt.Sprintf("node=%d os=%s device_type=%s", nodeID, os, typeIn))
	http.Redirect(w, r, "/admin/devices?ok=device_meta", http.StatusFound)
}

// PostAdminDevicesForceBackfillTags is the v0.33.1.20 "fix
// everything" admin action. It iterates every portal user and
// calls nodeownership.Backfill against the live headscale node
// list, which is the per-user helper that /my/devices already
// runs on every page load — but only for the CURRENT user. The
// admin's per-user backfill loop covers the cross-user case
// (e.g. operator fixes skyadmin's devices by loading
// /my/devices, but michail's devices still show "no dev-tag"
// until michail himself logs in).
//
// Why this exists (v0.33.1.20 motivation):
//   - On 2026-08-09 the operator reported 13 headscale nodes
//     with only `tag:private` (no `tag:dev-<user>-<host>`).
//     The per-user backfill in /my/devices had applied the
//     dev-tag to the 4 nodes the current user owned, but the
//     other 9 (michail's base/basic/nothing-phone-2/olesya +
//     skyadmin's emilia/sharlotta/karolina/skybars/skybars-1/
//     skygate-vm/msi/a71) only get the tag when the OWNING
//     user loads /my/devices. The admin button is the
//     operator-side escape hatch.
//   - Same loop is also the right vehicle for the rename
//     detection (v0.33.1.20 backfill change): the operator
//     runs it after the user reports a rename, the backfill
//     detects existing.hostname != n.Hostname and updates
//     the DB + UntagNode(oldTag) + AddTag(newTag).
//
// Idempotent — safe to spam. The per-user backfill is no-op
// for users whose nodes are already tagged, and rename
// detection only fires when the live headscale name actually
// differs from node_owner_map.hostname.
//
// Admin-only. The HS client is reused for every user (the
// tailnet is single-plane on this operator's install;
// multi-plane is future v0.34.x per the BACKLOG).
func (s *Service) PostAdminDevicesForceBackfillTags(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.HSGlobalFn == nil {
		// v0.33.1.20: defensive — the test harness (and
		// a possible misconfigured single-tenant deploy
		// without main.go's HSGlobalFn wiring) can leave
		// the callback nil. Calling a nil func value
		// panics; check FIRST so we surface a clean http.StatusInternalServerError
		// instead of crashing the goroutine.
		http.Error(w, "headscale client not configured", http.StatusInternalServerError)
		return
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		http.Error(w, "headscale client not configured", http.StatusInternalServerError)
		return
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		http.Error(w, "headscale list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := db.GetAllPortalUsers(s.DB)
	if err != nil {
		http.Error(w, "list users failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	hs.InvalidateCache()
	// Per-user pass. The backfill helper does its own
	// per-user preauth-key match + temporal fallback + GC
	// pass, so calling it once per user is the full coverage.
	processed := 0
	renamed := 0
	for _, u := range users {
		if u.Username == "" {
			continue
		}
		// Snapshot existing rows BEFORE the backfill runs
		// so we can count renames for the audit log
		// (existing.hostname != n.Hostname fires a rename).
		preRows, _ := db.ListNodeOwnersByUsername(s.DB, u.Username)
		preByNodeID := make(map[string]string, len(preRows))
		for _, r := range preRows {
			preByNodeID[r.NodeID] = r.Hostname
		}
		nodeownership.Backfill(s.DB, hs, nodes, u.ID, u.Username)
		// Re-read to count renames (the helper updates
		// the row in place; comparing pre vs post is the
		// audit-friendly way to surface "5 renames
		// applied" instead of a generic "backfill done").
		postRows, _ := db.ListNodeOwnersByUsername(s.DB, u.Username)
		for _, r := range postRows {
			if preHost, ok := preByNodeID[r.NodeID]; ok && preHost != "" && preHost != r.Hostname {
				renamed++
			}
		}
		processed++
	}
	s.Backend.Audit(c.UserID, c.Username, "force_backfill_tags",
		fmt.Sprintf("users=%d renames=%d", processed, renamed))
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/devices?ok=%s", url.QueryEscape(
			fmt.Sprintf("Force backfill: %d users processed, %d renames applied", processed, renamed))), http.StatusSeeOther)
}

// PostAdminDeviceTransfer is the v0.33.1.20 admin action for
// reassigning a node to a different portal user. It handles
// the orphan-row case (e.g. id=27 "svyatoslava" was assigned
// to skyadmin before svyatoslava was a real headscale user;
// svyatoslava's actual device is id=30). The operator
// resolves the conflict by clicking "Transfer to svyatoslava"
// on id=27's row in /admin/devices — the handler then:
//   1. Upserts the node_owner_map row with the new owner
//   2. UntagNode(oldTag) so headscale drops the stale
//      `tag:dev-<oldUser>-<oldHost>` (or just renames in
//      headscale if the hostname also needs to change)
//   3. AddTag(newTag) so headscale carries the new
//      `tag:dev-<newUser>-<oldHost>`
//
// The handler does NOT re-apply the ACL automatically —
// the operator must click "Re-apply ACL" on
// /admin/exit-rules to push the new tagOwners into the
// headscale policy (because AddTag alone succeeds but
// headscale's policy must INCLUDE the new tagOwners entry
// for the grant src to actually be valid; that's the
// /admin/exit-rules/reapply step). The redirect message
// tells the operator to do that as a follow-up.
//
// POST /admin/devices/transfer (form fields: node_id,
// target_username).
func (s *Service) PostAdminDeviceTransfer(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	nodeIDStr := r.FormValue("node_id")
	targetUsername := strings.TrimSpace(r.FormValue("target_username"))
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil || nodeID <= 0 {
		http.Error(w, "bad node id", http.StatusBadRequest)
		return
	}
	if targetUsername == "" {
		http.Error(w, "target_username required", http.StatusBadRequest)
		return
	}
	// Look up the target portal user.
	users, err := db.GetAllPortalUsers(s.DB)
	if err != nil {
		http.Error(w, "list users failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var target *db.User
	for i := range users {
		if users[i].Username == targetUsername {
			target = &users[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "target user not found: "+targetUsername, http.StatusBadRequest)
		return
	}
	// Read the current node_owner_map row FIRST (doesn't
	// need headscale) so a missing node returns http.StatusBadRequest instead
	// of the headscale-http.StatusInternalServerError that the v0.33.1.20 pre-check
	// would otherwise return. v0.33.1.20: the HS check used
	// to fire before the node check, which made the http.StatusInternalServerError vs
	// http.StatusBadRequest distinction confusing for the operator — "is the
	// node missing, or is my headscale down?".
	currentRow, err := db.GetNodeOwner(s.DB, nodeIDStr)
	if err != nil {
		http.Error(w, "node not in node_owner_map: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.HSGlobalFn == nil {
		// v0.33.1.20: defensive — see PostAdminDevicesForceBackfillTags
		// for the rationale (nil func value panics).
		http.Error(w, "headscale client not configured", http.StatusInternalServerError)
		return
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		http.Error(w, "headscale client not configured", http.StatusInternalServerError)
		return
	}
	// Pull the live hostname from headscale (n.Hostname
	// is the canonical Tailscale GivenName — the new
	// dev-tag must be built from THAT, not from the
	// current row's hostname, which may be stale after
	// a rename the admin didn't sync).
	var liveHostname string
	if nodes, err := hs.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == nodeIDStr {
				liveHostname = n.Hostname
				break
			}
		}
	}
	if liveHostname == "" {
		// Fall back to the row's hostname (e.g. node
		// is offline / not in headscale list right now).
		liveHostname = currentRow.Hostname
	}
	newDevTag := fmt.Sprintf("tag:dev-%s-%s", targetUsername, liveHostname)
	// 1) Upsert the row with the new owner + new dev tag.
	if err := db.UpsertNodeOwner(s.DB, nodeIDStr, target.HeadscaleUserID, targetUsername, newDevTag, c.UserID); err != nil {
		http.Error(w, "db upsert failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// 2) UntagNode the OLD dev-tag (if any) so headscale
	//    doesn't accumulate both old+new. Skip when the
	//    old tag was the same as the new one (same
	//    user+host, idempotent) or when the old tag
	//    wasn't a dev-tag (tag:private etc — leave it).
	if currentRow.Tag != "" && currentRow.Tag != newDevTag && strings.HasPrefix(currentRow.Tag, "tag:dev-") {
		if err := hs.UntagNode(nodeID, currentRow.Tag); err != nil {
			// Non-fatal — the stale tag stays in
			// headscale, but the row in node_owner_map
			// is correct and the next ACL re-apply
			// will surface the stale tagOwners. Log
			// via audit so the operator can clean up.
			s.Backend.Audit(c.UserID, c.Username, "device_transfer_untag_failed",
				fmt.Sprintf("node=%d old_tag=%s err=%v", nodeID, currentRow.Tag, err))
		}
	}
	// 3) AddTag the new dev-tag. Idempotent (no-op if
	//    the node already has it).
	if err := hs.AddTag(nodeID, newDevTag); err != nil {
		// Non-fatal — the DB row is correct, the headscale
		// tag will land on the next /admin/devices/force-
		// backfill-tags click. Audit the failure so the
		// operator knows to re-run.
		s.Backend.Audit(c.UserID, c.Username, "device_transfer_addtag_failed",
			fmt.Sprintf("node=%d new_tag=%s err=%v", nodeID, newDevTag, err))
	}
	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "device_transfer",
		fmt.Sprintf("node=%d from=%s to=%s new_tag=%s", nodeID, currentRow.Username, targetUsername, newDevTag))
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/devices?ok=%s", url.QueryEscape(
			fmt.Sprintf("Node %d transferred to %s (tag=%s). Click 'Re-apply ACL' on /admin/exit-rules to push the new tagOwners.", nodeID, targetUsername, newDevTag))), http.StatusSeeOther)
}
