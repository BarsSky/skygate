// devices_reregister.go — POST /my/devices/{id}/reregister
// (B-mod-reregister, 2026-09-13): the escape hatch for ghost nodes
// that headscale has reassigned to the tagged-devices sentinel.
// Split out of devices.go (work order item 5, 2026-10-07) —
// pure move, no behaviour change.
package my

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/devicedelete"
)

// PostMyDeviceReregister (B-mod-reregister, 2026-09-13) is the
// escape hatch for ghost nodes that headscale has reassigned to
// the synthetic "tagged-devices" sentinel user (id=2147455555).
// The pattern that triggers this: preauth keys were issued at
// install time WITHOUT a `--user` flag (or via a headscale API
// path that defaulted to the sentinel) — the resulting nodes have
// user.name = "tagged-devices", no preAuthKey link, no machine key,
// and never show up in node_owner_map via Backfill's Strategy A
// (preauth match) or Strategy C (temporal match). The per-user
// ACL grants referencing the device never apply.
//
// The fix flow:
//  1. headscale nodes delete -i <id> --force — remove the ghost.
//  2. Issue a fresh preauth key bound to the CURRENT portal user
//     (the user's hsUserID is captured here, so the new node
//     WILL be attributed to this user in headscale).
//  3. Show the user the new key on the existing preauth_result
//     page (with a banner explaining "this is the re-register
//     key for <hostname>" so the user knows which device it
//     goes with).
//  4. When the device reconnects with this new key, Backfill's
//     Strategy A picks up the node → preauth_keys match → row
//     in node_owner_map → B77 tag-autoupdate applies
//     tag:dev-<user>-<hostname> → per-device ACL grants work.
//
// Scope (mirrors PostMyDeviceDelete):
//   - User must own the node via LIVE list (n.UserName ==
//     c.Username) OR via the node_owner_map snapshot.
//   - Re-register is ONLY valid for nodes in the tagged-devices
//     sentinel user. Refuses nodes that already have a real
//     user (the operator should use PostMyDeviceDelete instead).
//   - Cross-user attempts return 404 (same as delete).
//
// Side effects:
//  1. hsClient.DeleteNode(nodeID) — headscale gRPC, idempotent
//     (treats "no longer exists" as success per B171).
//  2. devicedelete.Delete(...) — full local cleanup.
//  3. CreatePreauthKey(hsUserID, "24h", true) — fresh 24h
//     REUSABLE key.
//  4. InsertPreauthKey(...) — persist key.
//  5. AppendAuditLog action="device_reregister".
//  6. Render preauth_result.html.
func (s *Service) PostMyDeviceReregister(w http.ResponseWriter, r *http.Request) {
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
	lang := s.I18n.LangFromRequest(r)

	hsUserID, _, herr := db.GetUserHSByID(s.dbc(), c.UserID)
	if herr != nil || !hsUserID.Valid {
		log.Printf("web.my.reregister: GetUserHSByID userID=%d err=%v", c.UserID, herr)
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}

	hsClient := s.Backend.HSForUserFn(c.UserID)
	allNodes, lerr := hsClient.ListAllNodes()
	if lerr != nil {
		log.Printf("web.my.reregister: ListAllNodes userID=%d err=%v", c.UserID, lerr)
		http.Error(w, "headscale unreachable", http.StatusBadGateway)
		return
	}

	host := ""
	owned := false
	isTaggedGhost := false
	for _, n := range allNodes {
		if n.ID == idStr {
			host = n.Hostname
			if n.UserName == c.Username {
				owned = true
				isTaggedGhost = false
			} else if n.UserName == "tagged-devices" {
				snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
				for _, sid := range snapIDs {
					if sid == idStr {
						owned = true
						isTaggedGhost = true
						break
					}
				}
			}
			break
		}
	}
	if !owned {
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
		for _, sid := range snapIDs {
			if sid == idStr {
				owned = true
				isTaggedGhost = true
				break
			}
		}
	}
	if !owned {
		log.Printf("web.my.reregister: node %s not owned by userID=%d", idStr, c.UserID)
		http.Error(w, s.I18n.T(lang, "devices.delete_err_404"), http.StatusNotFound)
		return
	}
	if !isTaggedGhost {
		http.Error(w, "device belongs to a real user — use Delete instead", http.StatusBadRequest)
		return
	}
	if host == "" {
		owners, _ := db.ListNodeOwnersByUsername(s.dbc(), c.Username)
		for _, o := range owners {
			if o.NodeID == idStr {
				host = o.Hostname
				break
			}
		}
	}

	headscaleAlreadyGone := false
	if derr := hsClient.DeleteNode(nodeID); derr != nil {
		msg := derr.Error()
		if strings.Contains(msg, "no longer exists in NodeStore") ||
			strings.Contains(msg, "node not found") {
			headscaleAlreadyGone = true
		} else {
			log.Printf("web.my.reregister: DeleteNode id=%s err=%v", idStr, derr)
			http.Error(w, s.I18n.Tf(lang, "devices.delete_err_failed", derr.Error()), http.StatusInternalServerError)
			return
		}
	}
	deps := devicedelete.Deps{
		DB:          s.dbc(),
		HS:          hsClient,
		Cfg:         s.Cfg,
		Username:    c.Username,
		AuditDetail: fmt.Sprintf("user_device_reregister id=%s hostname=%s", idStr, host),
		AuditFn: func(action, detail string) {
			if s.Backend != nil {
				s.Backend.Audit(c.UserID, c.Username, action, detail)
			}
		},
	}
	if _, derr := devicedelete.Delete(r.Context(), deps, nodeID, host, c.Username); derr != nil {
		log.Printf("web.my.reregister: devicedelete.Delete err=%v (continuing)", derr)
	}
	if headscaleAlreadyGone {
		http.Error(w, "device already gone — issue a fresh key via /my/preauth", http.StatusGone)
		return
	}

	expirationStr := "24h"
	ttlSeconds := 24 * 3600
	key, kerr := hsClient.CreatePreauthKey(hsUserID.Int64, expirationStr, true)
	if kerr != nil {
		log.Printf("web.my.reregister: CreatePreauthKey err=%v", kerr)
		http.Error(w, s.I18n.Tf(lang, "devices.reregister_err_key_failed", kerr.Error()), http.StatusInternalServerError)
		return
	}
	hsClient.InvalidateCache()

	now := time.Now()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second).Unix()
	// R7 (2026-09-18): used to log-and-continue, which handed the user a
	// key with no local row — the re-registered device then never got
	// attributed back to its owner. persistIssuedKey fails hard and revokes
	// the headscale key as compensation.
	if _, perr := s.persistIssuedKey(c.UserID, hsUserID.Int64, key, expiresAt, "web.my.reregister"); perr != nil {
		http.Redirect(w, r, "/my/devices?err="+url.QueryEscape(
			s.I18n.T(lang, "preauth.persist_failed")), http.StatusSeeOther)
		return
	}
	detail := fmt.Sprintf("old_node_id=%s hostname=%s new_preauth_id=%s ttl=%s", idStr, host, key.ID, expirationStr)
	if aerr := db.AppendAuditLog(s.dbc(), c.UserID, c.Username, "device_reregister", detail); aerr != nil {
		log.Printf("web.my.reregister: AppendAuditLog err=%v", aerr)
	}

	s.Backend.RenderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":             key.Key,
		"Expires":         humanizeTTL(int64(ttlSeconds)),
		"OS":              r.FormValue("os"),
		"OSLabel":         osLabel(r.FormValue("os")),
		"ReissueFrom":     0,
		"ReissueTo":       0,
		"ReregisteredFor": host,
	})
}
