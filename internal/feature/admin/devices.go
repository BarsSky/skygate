package admin

// devices.go — admin devices page (/admin/devices) + tag/untag/sync.
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/handlers_admin_nodes.go.
//
// Handlers: GetAdminDevices, PostAdminDevicesSyncFromHeadscale,
// PostAdminNodeTag, PostAdminNodeUntag. Helper: nodeTagRefusedForUserDevice
// (pure function, unit-tested in the original handlers).

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/devicemeta"
	"skygate/internal/headscale"
)

// GetAdminDevices renders the /admin/devices page — all headscale
// nodes across the tailnet, with per-device ACL tags and the
// available-exit-node list for the per-device preferred-exit
// dropdown. Admin-only.
func (s *Service) GetAdminDevices(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
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
	s.Backend.RenderWithLayout(w, r, "admin/devices.html", c, map[string]any{
		"Nodes":             deviceRows,
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
	})
}

// PostAdminDevicesSyncFromHeadscale is the v0.14.0 "Sync from
// headscale" button. INSERTs any missing rows in node_owner_map
// + UPDATEs drifted tags. Admin-only.
func (s *Service) PostAdminDevicesSyncFromHeadscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		http.Error(w, "headscale list failed: "+err.Error(), 500)
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
		http.Error(w, "sync failed: "+err.Error(), 500)
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
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
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
		http.Error(w, err.Error(), 500)
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
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}
	hs := s.HSGlobalFn()
	if err := hs.UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
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
		http.Error(w, "forbidden", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), 400)
		return
	}
	idStr := r.FormValue("node_id")
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	os := strings.TrimSpace(r.FormValue("os"))
	typeIn := strings.TrimSpace(r.FormValue("device_type"))
	if !devicemeta.IsOSValid(os) {
		http.Error(w, "invalid os: "+os, 400)
		return
	}
	if !devicemeta.IsTypeValid(typeIn) {
		http.Error(w, "invalid device_type: "+typeIn, 400)
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
		http.Error(w, "db write failed: "+err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "device_meta_set",
		fmt.Sprintf("node=%d os=%s device_type=%s", nodeID, os, typeIn))
	http.Redirect(w, r, "/admin/devices?ok=device_meta", http.StatusFound)
}
