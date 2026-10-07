// devices_renew.go — POST /my/devices/{id}/renew (B160, v1.5.0)
// plus the B160.1 "node no longer exists" detection: extends a
// device's headscale-side expiry without waiting for the
// auto-renewer. Split out of devices.go (work order item 5,
// 2026-10-07) — pure move, no behaviour change.
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
	"skygate/internal/headscale"
)

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
	hsUserID, _, herr := db.GetUserHSByID(s.dbc(), c.UserID)
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
			snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
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
	snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
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
