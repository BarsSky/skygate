package handlers

// handlers_admin_nodes.go — extracted from handlers.go.
// Admin devices page: list of all nodes across portal users, plus the
// tag/untag actions that apply headscale tags. Kept separate because these
// handlers reach into the headscale admin API (TagNode/UntagNode via
// CLI fallback) rather than the per-user portal flow in handlers.go.

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

func (a *App) GetAdminDevices(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	// 2026-07-15: v0.12.0 — admin pages always use the global
	// headscale (HSGlobal). Per-user routing on /admin/devices
	// would be ambiguous ("show devices of which user?"); the
	// admin view is the operator's-eye view of the primary
	// control plane.
	users, _ := a.HSGlobal().ListUsers()
	allNodes, _ := a.HSGlobal().ListAllNodes()
	// 2026-07-24: v0.28.0 — admin page surfaces the
	// per-device ACL tag (tag:dev-<user>-<hostname>) for
	// every node in node_owner_map. The list is small
	// (one row per device, currently ~13), and a flat list
	// keeps the template lookup O(1) via a map.
	devTags, _ := db.GetPerUserDeviceTags(a.DB, "")
	devTagMap := make(map[string]string, len(devTags))
	for _, t := range devTags {
		// Key by hostname — admin/devices.html iterates
		// Nodes (headscale view) and looks up
		// DevTagMap[.Hostname]. Hostname is unique across
		// the tailnet (Tailscale rejects duplicates), so
		// the map is 1:1.
		devTagMap[t.Hostname] = t.Tag
	}
	// 2026-07-25: v0.28.4 — admin view also surfaces the
	// per-device preferred exit-node (set via
	// /admin/devices/preferred-exit). Keyed by
	// "<skygate_user_id>:<lowercased hostname>" so the
	// template can disambiguate devices with the same
	// hostname across users (rare but possible — the
	// per-user keying is intentional).
	//
	// We map headscale username → skygate user_id because
	// NodeView exposes the headscale username (n.UserName),
	// not the skygate user_id. BUT — and this is critical —
	// after headscale applies tag:private to a node, it
	// REASSIGNS ownership to the synthetic "tagged-devices"
	// user. So n.UserName is "tagged-devices" for most
	// user-owned devices, and a direct username→user_id
	// map won't work.
	//
	// The fix: derive the skygate user from the
	// per-device ACL tag (tag:dev-<user>-<device>) which
	// skygate's backfillNodeOwnership sets. The dev tag
	// is the authoritative owner link — it survives
	// headscale's tag-driven ownership reassignment.
	// For each (hostname) we already have in devTagMap,
	// parse the username out of the dev tag.
	deviceExitPrefs, _ := db.ListAllDeviceExitNodePrefs(a.DB)
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
		if err := a.DB.QueryRow(
			`SELECT id FROM portal_users WHERE username = ?`, name,
		).Scan(&id); err == nil {
			skygateUserByName[name] = id
		}
	}
	// skygateUserByHost is keyed on lowercased hostname.
	// The template uses it directly — no string parsing
	// in Go templates. The value is the skygate user_id
	// derived from the per-device ACL tag (tag:dev-<user>-
	// <device>), which is the authoritative owner link
	// post-headscale-reassignment.
	skygateUserByHost := make(map[string]int64, len(devTags))
	for _, dt := range devTags {
		// dt.Tag is "tag:dev-<user>-<device-lowercased>".
		// Strip the "tag:dev-" prefix and the device
		// suffix to get the username.
		const prefix = "tag:dev-"
		if !strings.HasPrefix(dt.Tag, prefix) {
			continue
		}
		rest := strings.TrimPrefix(dt.Tag, prefix)
		// rest is "<user>-<device>" — split on the LAST
		// "-" so usernames containing "-" (rare but
		// possible) still resolve correctly.
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
	// The admin can also set per-device prefs; we need
	// the list of available exit-nodes for the dropdown
	// (same as /my/devices — the public nodes with
	// tag:exit-node).
	exits, _ := a.HSGlobal().ListExitNodes()
	// Per-user exit prefs (v0.28.1) for the "User's
	// default" hint column.
	userExitPrefs, _ := db.ListAllUserExitNodePrefs(a.DB)
	userExitPrefMap := make(map[string]string, len(userExitPrefs))
	for _, ep := range userExitPrefs {
		userExitPrefMap[strconv.FormatInt(ep.UserID, 10)] = ep.ExitNodeTag
	}
	a.renderWithLayout(w, r, "admin/devices.html", c, map[string]any{
		"Nodes":             allNodes,
		"Users":             users,
		"FlashSuccess":      r.URL.Query().Get("ok"),
		"FlashError":        r.URL.Query().Get("err"),
		"DevTagMap":         devTagMap,
		"DeviceExitPrefMap": deviceExitPrefMap,
		"DeviceExitViaMap":  deviceExitViaMap,
		"AvailableExits":    exits,
		"UserExitPrefMap":   userExitPrefMap,
		"SkygateUserByName": skygateUserByName,
		// 2026-07-25: v0.28.4 — keyed on lowercased
		// hostname. The template uses it directly to
		// look up the per-device pref and to render
		// the user_id form field.
		"SkygateUserByHost": skygateUserByHost,
	})
}

// PostAdminDevicesSyncFromHeadscale is the v0.14.0
// "Sync from headscale" button on /admin/devices. Admin-only.
// Calls db.SyncNodesFromHeadscale to INSERT any missing
// rows + UPDATE drifted tags. This is the operator's
// escape hatch when:
//   1. They tagged a relay directly in headscale (the bot's
//      /exit_nodes then reports "no nodes found" until
//      this button is clicked).
//   2. The bot's per-tick auto-heal in commands_user.go is
//      off (e.g. SKYGATE_BOT_AUTO_HEAL_TAGS=false), so the
//      cache is stale.
//
// 2026-07-15: v0.14.0 — also wired to the bot's /sync_nodes
// admin command (same DB call, different entry point).
func (a *App) PostAdminDevicesSyncFromHeadscale(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	nodes, err := a.HSGlobal().ListAllNodes()
	if err != nil {
		http.Error(w, "headscale list failed: "+err.Error(), 500)
		return
	}
	var syncInfos []db.SyncNodeInfo
	for _, n := range nodes {
		// Pick the first non-empty tag for the row. headscale
		// returns a slice; we treat "tag:exit-node" as the
		// most specific (it's what the bot reads) and fall
		// back to whatever else is set.
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
	ins, upd, err := db.SyncNodesFromHeadscale(a.DB, syncInfos)
	if err != nil {
		http.Error(w, "sync failed: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "node_sync_from_headscale",
		fmt.Sprintf("inserted=%d updated=%d", ins, upd))
	// Redirect to /admin/devices with a flash that the
	// template renders as the success banner. We use the
	// dedicated ?ok=... query param that PostAdminNodeTag
	// / PostAdminNodeUntag already use (the page reads it).
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/devices?ok=%s", url.QueryEscape(
			fmt.Sprintf("Sync from headscale: %d inserted, %d updated", ins, upd))), http.StatusSeeOther)
}

// PostAdminNodeTag adds a headscale tag to a node.
func (a *App) PostAdminNodeTag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	if nodes, err := a.HSGlobal().ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				origUserID = n.UserID
				origUserName = n.UserName
				nodeTags = n.Tags
				break
			}
		}
	}

	// 2026-07-28: v0.30.1 — refuse to put an exit-node-like tag
	// on a per-user device. See nodeTagRefusedForUserDevice for
	// the full rationale; the short version: a user device
	// accidentally promoted to "exit-node" appears in every
	// Tailscale client's exit-node menu, and auto-failover
	// (lowest metric = self, 0ms) sends all traffic to /dev/null.
	if refused, msg, hadTag := nodeTagRefusedForUserDevice(nodeID, tag, nodeTags); refused {
		a.audit(c.UserID, c.Username, "node_tag_refused",
			fmt.Sprintf("node=%d attempted_tag=%s reason=user_device node_had=%s",
				nodeID, tag, hadTag))
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	if err := a.HSGlobal().TagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// 2026-07-15: Этап 14 v13 — v0.10.13 fix. The old guard
	// "origUserName != \"tagged-devices\"" skipped the node_owner_map
	// update for nodes whose headscale ownership was reassigned to
	// the synthetic tagged-devices user (which happens automatically
	// when any tag is applied to a node in headscale). The result
	// was that admin-tagged devices kept their old tag:untagged
	// row in skygate, so the bot's /nodes (which reads from
	// node_owner_map) showed the wrong tag. The bot now self-heals
	// on read via db.SyncTagsFromHeadscale, but we also fix the
	// source here: when the origUserName is "tagged-devices" we
	// look up the existing row in node_owner_map (by node_id) and
	// UPDATE only the tag, leaving username + headscale_user_id
	// alone so a portal-side owner link is preserved.
	if origUserID != "" && origUserName != "" {
		nodeIDStr := strconv.FormatInt(nodeID, 10)
		var hsUID int64
		if n, err := strconv.ParseInt(origUserID, 10, 64); err == nil {
			hsUID = n
		}
		if origUserName == "tagged-devices" {
			// Preserve the existing portal-side owner. The new
			// tag is the source of truth (admin just set it on
			// headscale), the username + headscale_user_id stay
			// as they were.
			_ = db.UpdateNodeOwnerTag(a.DB, nodeIDStr, tag, c.UserID)
		} else {
			_ = db.UpsertNodeOwner(a.DB, nodeIDStr, hsUID, origUserName, tag, c.UserID)
		}
	}

	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "node_tag", fmt.Sprintf("node=%d tag=%s owner=%s", nodeID, tag, origUserName))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

func (a *App) PostAdminNodeUntag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	if err := a.HSGlobal().UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// 2026-07-12: Этап 10 part 4 — moved to db.DeleteNodeOwnerByNodeTag.
	_ = db.DeleteNodeOwnerByNodeTag(a.DB, strconv.FormatInt(nodeID, 10), tag)

	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
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
// Why the guard exists: on 2026-07-28, michail's Windows box
// "base" (headscale id=7, tag:dev-michail-base) was found
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
//
// To make the same node a relay later: first untag the
// per-user tag via PostAdminNodeUntag, then re-tag as
// tag:exit-node.
func nodeTagRefusedForUserDevice(nodeID int64, requestedTag string, currentTags []string) (refused bool, message string, existingDevTag string) {
	// Only exit-node-like tags trigger the guard. Other tags
	// (tag:public, tag:private, tag:subnet-router) are fine to
	// apply to per-user devices — they're ACL primitives, not
	// exit-node candidates.
	if !strings.HasPrefix(requestedTag, "tag:exit") {
		return false, "", ""
	}
	// Find any per-user device tag in the node's current tag
	// set. tag:dev-<user>-<device> is the authoritative
	// per-user device marker (v0.28.0+). tag:dev- prefix is
	// stable; we don't try to parse the user/device out of it
	// here — just check the prefix.
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
