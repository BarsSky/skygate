// devices_delete.go — POST /my/devices/{id}/delete (B162, v1.5.1;
// B171 comprehensive delete): per-row device delete from
// /my/devices. Split out of devices.go (work order item 5,
// 2026-10-07) — pure move, no behaviour change.
package my

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/devicedelete"
)

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
//  1. `headscale nodes delete -i <id>` via the gRPC client
//     (InvalidateCache() runs inside DeleteNode).
//  2. `DELETE FROM node_owner_map WHERE node_id=$1` so the
//     next /my/devices load doesn't re-render the row from
//     the snapshot (the snapshot branch would otherwise
//     keep showing the device until the admin manually
//     intervenes).
//  3. Audit log row `device_deleted node_id=N hostname=H`.
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
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
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
		owners, _ := db.ListNodeOwnersByUsername(s.dbc(), c.Username)
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
	// still run the comprehensive local cleanup
	// (otherwise the local snapshot + device_rules
	// would keep naming the deleted device, and
	// the next ACL regen would crash headscale's
	// SetPolicy).
	lang := s.I18n.LangFromRequest(r)
	headscaleAlreadyGone := false
	if derr := hsClient.DeleteNode(nodeID); derr != nil {
		msg := derr.Error()
		if strings.Contains(msg, "no longer exists in NodeStore") ||
			strings.Contains(msg, "node not found") {
			headscaleAlreadyGone = true
			log.Printf("web.my.delete: node=%s no longer exists in headscale (B162 + B171): %s", idStr, msg)
			// Fall through to devicedelete.Delete
			// — the local DB still has stale rows
			// (node_owner_map, device_rules, etc.)
			// that must be cleaned + the ACL must be
			// regenerated. Skipping this would leave
			// the user with a ghost node in
			// /my/exit-rules and a stale policy in
			// headscale.
		} else {
			log.Printf("web.my.delete: DeleteNode id=%s err=%v", idStr, derr)
			http.Error(w, s.I18n.Tf(lang, "devices.delete_err_failed", derr.Error()), http.StatusInternalServerError)
			return
		}
	}
	// B171 (v1.5.2) — comprehensive cleanup. Both
	// the success path (headscale.DeleteNode
	// returned nil) and the "already gone" path
	// above reach this line, so the local DB +
	// device_rules + ACL regen happen consistently
	// regardless of how the deletion was triggered.
	// The shared helper (internal/devicedelete)
	// owns the cleanup logic so the admin path
	// (PostAdminDeviceDelete) can call the same
	// function with admin-scoped dependencies.
	deps := devicedelete.Deps{
		DB:          s.dbc(),
		HS:          hsClient,
		Cfg:         s.Cfg,
		Username:    c.Username,
		AuditDetail: fmt.Sprintf("user_device_delete id=%s hostname=%s", idStr, host),
		AuditFn: func(action, detail string) {
			if s.Backend != nil {
				s.Backend.Audit(c.UserID, c.Username, action, detail)
			}
		},
	}
	cleanRes, _ := devicedelete.Delete(r.Context(), deps, nodeID, host, c.Username)
	if headscaleAlreadyGone {
		// 410 Gone — the node was already removed
		// from headscale (operator action via the
		// headscale CLI, or a parallel admin
		// delete). The local DB is now clean, but
		// the user-visible message is the B160.1
		// "already removed" copy so the user
		// doesn't see a contradictory "device
		// deleted" flash next to a 410.
		http.Error(w, s.I18n.T(lang, "devices.delete_err_deleted"), http.StatusGone)
		return
	}

	// Success path: redirect with a flash that
	// shows the rules-cleaned count so the user
	// knows the ACL was also regenerated. The
	// deleted=... query param keeps the B162
	// backwards-compat (the pre-B171 /my/devices
	// template already reads deleted= to render a
	// toast). The new deleted_rules=N param is
	// read by the B171 template update.
	rulesCount := cleanRes.RulesDeleted
	redirect := fmt.Sprintf("/my/devices?deleted=%s&deleted_rules=%d",
		url.QueryEscape(host), rulesCount)
	if !cleanRes.ACLRegen.Applied && cleanRes.ACLRegen.Err != nil {
		// The device is gone but the ACL regen
		// failed. Surface the error to the user
		// so they know to check /admin/audit
		// (the audit row already records the
		// failure with full detail).
		redirect += "&acl_err=" + url.QueryEscape(cleanRes.ACLRegen.Err.Error())
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}
