// exit_nodes_reconnect_b501.go — B501 (2026-11-19): the per-row «Переподключить» action.
//
// POST /admin/exit-nodes/{hostname}/reconnect
//
// What it does: restarts the relay's tailscaled through exit_rules.Service.ReconnectRelay
// (fixed command, proven transport ladder, B451-style verify-read afterwards) and puts the
// DERP region it came back on into the flash banner. Admin-only, audited.
//
// Why it exists at all: a relay holding a stale netmap stops being a DERP candidate while
// still answering ping — measured live as `karolina` reporting `Nearest DERP: New York
// City` with the operator's own region 900 healthy. Until now the fix was an SSH session,
// so it was neither repeatable nor visible in the audit log.
//
// The honest boundary is spelled out here, not in the docs: a DEVICE cannot be reconnected
// from the panel (skygate never reaches a client's daemon). The page shows this action only
// on relay rows, and `scripts/device_connectivity.sh` is what answers the device case.
package admin

import (
	"fmt"
	"net/http"
	"net/url"
)

func (s *Service) PostAdminExitNodeReconnect(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.ReconnectRelayFn == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("reconnect is not wired (B501)"), http.StatusSeeOther)
		return
	}
	hostname := r.PathValue("hostname")
	if hostname == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("hostname path var is empty"), http.StatusSeeOther)
		return
	}
	res := s.ReconnectRelayFn(hostname)
	// The audit row carries the OUTCOME, not the intent: `ok` only when the relay answered
	// afterwards, plus the region, because "reconnected" without a region is a guess.
	auditDetail := fmt.Sprintf("hostname=%s ok=%t via=%s endpoint=%s region=%s online=%t err=%s",
		hostname, res.OK, res.Via, res.Endpoint, res.Region, res.Online, res.Err)
	s.Backend.Audit(c.UserID, c.Username, "exit_relay_reconnected", auditDetail)
	if !res.OK {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(res.Summary()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(res.Summary()), http.StatusSeeOther)
}
