// exit_nodes_handlers.go — the write paths of /admin/exit-nodes.
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01): health-now, add,
// delete, "use the Tailscale IP", the two re-sync actions and the
// accept-routes toggle. Each one is admin-only and each one ends in an audit row
// plus a redirect — keeping them together makes that pattern auditable at a
// glance.

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// PostAdminExitNodesHealthNow (v0.13.0) is the "Run health
// check now" button on /admin/exit-nodes. Admin-only. Calls
// ExitNodeMonitor.CheckNow synchronously (the monitor's
// internal mutex serialises concurrent admin clicks) and
// redirects back to /admin/exit-nodes so the operator sees
// the fresh state. The background goroutine is unaffected
// (it runs on its own ticker, not through CheckNow).
//
// We redirect to /admin/exit-nodes directly (not via the
// shared redirectWithFlash helper, which is hard-coded to
// /admin/telegram) so a successful run lands the operator
// back on the page they were just on.
//
// If the monitor is disabled
// (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off) or hasn't been
// wired (e.g. running unit tests), the handler shows a
// flash error instead of crashing.
func (s *Service) PostAdminExitNodesHealthNow(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.ExitNodeMonitor == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Exit-node monitor is disabled (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off)"), http.StatusSeeOther)
		return
	}
	if err := s.ExitNodeMonitor.CheckNow(r.Context()); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Health check failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_health_now", "")
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Health check completed."), http.StatusSeeOther)
}

// PostAdminExitNodesAdd handles the "Add exit node" form.
// Admin-only.
func (s *Service) PostAdminExitNodesAdd(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	sshTarget := strings.TrimSpace(r.FormValue("ssh_target"))
	sshKey := strings.TrimSpace(r.FormValue("ssh_key_path"))
	// v0.33.1.33 B85: per-row non-default SSH port. Empty
	// string is preserved through to the B81 auto-fallback
	// (no port suffix, port 22 default). The form pre-fills
	// with the empty string (see the form helper text), so
	// operators who don't need a non-default port don't have
	// to touch this field.
	sshPort := strings.TrimSpace(r.FormValue("ssh_port"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if nodeID == "" || hostname == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id and hostname are required"), http.StatusSeeOther)
		return
	}
	// B266 (2026-09-19): validate the SSH target and key path AT WRITE
	// TIME. Pre-B266 the form accepted anything and the value was
	// appended positionally to the ssh argv in
	// internal/headscale/routes.go — a target like
	// `-oProxyCommand=<cmd>` became an ssh OPTION and ran inside the
	// skygate container (which holds /var/run/docker.sock). The
	// headscale side now refuses unsafe targets too; this check exists
	// so the operator gets an immediate, specific message instead of a
	// row that silently fails on every sync.
	if sshTarget != "" && !headscale.IsSafeSSHTarget(sshTarget) {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"ssh_target: ожидается [user@]host[:port] — без пробелов и без ведущего дефиса (получено "+sshTarget+")"), http.StatusSeeOther)
		return
	}
	if sshKey != "" && !strings.HasPrefix(sshKey, "/") {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"ssh_key_path должен быть абсолютным путём внутри контейнера (например /ssh-sync/skygate_sync)"), http.StatusSeeOther)
		return
	}
	acceptRoutes := 0
	switch strings.TrimSpace(r.FormValue("accept_routes")) {
	case "true":
		acceptRoutes = 1
	case "false":
		acceptRoutes = -1
	}
	// 2026-07-12: Этап 10 part 5 — moved to db.UpsertExitServer.
	if err := db.UpsertExitServer(s.dbc(), nodeID, hostname, sshTarget, sshKey, desc, sshPort, acceptRoutes); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_add", fmt.Sprintf("node=%s ssh=%s", hostname, sshTarget))
	http.Redirect(w, r, "/admin/exit-nodes?added=1", http.StatusFound)
}

// PostAdminExitNodesDelete handles the "Delete exit node" form.
// Admin-only.
func (s *Service) PostAdminExitNodesDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	nodeID := r.FormValue("node_id")
	if nodeID == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id is required"), http.StatusSeeOther)
		return
	}
	// 2026-07-12: Этап 10 part 5 — moved to db.DeleteExitServerByNodeID.
	if err := db.DeleteExitServerByNodeID(s.dbc(), nodeID); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_delete", nodeID)
	http.Redirect(w, r, "/admin/exit-nodes?deleted=1", http.StatusFound)
}

// PostAdminExitNodeUseTailscaleIP (v0.33.1.29 B81) is the
// "Use Tailscale IP" inline button on each /admin/exit-nodes
// table row. The button is only rendered when the stored
// ssh_target differs from the B81-resolved target (i.e. the
// operator has set ssh_target to a public IP that's now
// firewalled, or any other case where the B81 fallback
// silently overrides their override). Clicking the button
// overwrites ssh_target with "root@<tailscale_ip>" so the
// next sync uses the auto-fallback value explicitly (and
// the row's "auto" badge disappears).
//
// Admin-only. No-op on missing rows / missing tailscale_ip
// (the button isn't rendered in those cases).
func (s *Service) PostAdminExitNodeUseTailscaleIP(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	if nodeID == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id is required"), http.StatusSeeOther)
		return
	}
	// Read the existing row so we can preserve ssh_key_path /
	// description / accept_routes / ssh_port (the per-row
	// admin settings that UpsertExitServer would otherwise
	// blank out). v0.33.1.33 B85: ssh_port is also preserved
	// here — the operator's per-row non-default port (e.g.
	// karolina on 18022) must survive a "Use Tailscale IP"
	// click. The button changes ssh_target from
	// "root@karolina.example.com:18022" to "root@100.64.0.2",
	// but the port stays in the dedicated ssh_port column and
	// gets re-appended by the B81 auto-fallback:
	// "root@100.64.0.2:18022".
	var hostname, sshKeyPath, description, sshPort string
	var acceptRoutes int
	var enabled bool
	err := s.dbc().QueryRow(
		`SELECT hostname, COALESCE(ssh_key_path, ''), COALESCE(description, ''), COALESCE(ssh_port, ''), COALESCE(accept_routes, 0), enabled
		 FROM exit_servers WHERE node_id = $1`, nodeID,
	).Scan(&hostname, &sshKeyPath, &description, &sshPort, &acceptRoutes, &enabled)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("exit_servers row not found"), http.StatusSeeOther)
		return
	}
	if !enabled {
		// Don't touch disabled rows — the operator has explicitly
		// turned this exit node off, and overwriting ssh_target
		// would change its "off" state into a state that
		// participates in the next sync.
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Use Tailscale IP: node is disabled"), http.StatusSeeOther)
		return
	}
	// Use the new B81 helper to resolve "root@<tailscale_ip>" —
	// the same string the SyncAdvertisedRoutes call would
	// construct on the next tick. If neither ssh_target nor
	// tailscale_ip is set, the helper returns "" and we
	// short-circuit with a clear error (instead of writing
	// a malformed ssh_target = "root@").
	resolved, _ := db.LookupExitServerSSHTarget(s.dbc(), hostname)
	if resolved == "" || !strings.HasPrefix(resolved, "root@") {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Use Tailscale IP: no Tailscale IP discovered yet (wait for /admin/exit-nodes to refresh discovery)"), http.StatusSeeOther)
		return
	}
	if err := db.UpsertExitServer(s.dbc(), nodeID, hostname, resolved, sshKeyPath, description, sshPort, acceptRoutes); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_use_tailscale_ip",
		fmt.Sprintf("node=%s ssh_target=%s", hostname, resolved))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("SSH target set to Tailscale IP: "+resolved), http.StatusSeeOther)
}

// PostAdminExitNodesSync triggers a full advertised-routes
// sync (delegates to the SyncRoutes callback wired from
// cmd/skygate/main.go). Returns JSON for the "Sync now" button.
func (s *Service) PostAdminExitNodesSync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		// 2026-09-18 (R6): this endpoint is consumed by fetch() (the
		// "Sync now" button), and http.Error forces Content-Type:
		// text/plain — so the JS caller parsed a plain-text body as JSON
		// and fell back to a generic error. Set the header explicitly.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		return
	}
	if s.SyncRoutes == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"sync not wired"}`))
		return
	}
	result := s.SyncRoutes()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// PostAdminExitNodeSync is the B132 per-row "Re-sync" button
// handler. Takes a hostname from the URL path
// (POST /admin/exit-nodes/{hostname}/sync) and re-runs the
// sync just for that one node. Redirects back to
// /admin/exit-nodes with a flash message (ok=... or err=...)
// in the query string, like every other admin POST handler.
//
// 2026-08-18 (B132): the per-row tool was missing — the
// operator had to use the global "Sync all" which re-runs
// SetAdvertisedRoutes on every node and re-masks the actual
// per-node SSH error (e.g. emilia's ssh_target=public IP
// was timing out while karolina worked fine, but a global
// sync would re-fail emilia AND re-do karolina's no-op work).
// The per-row button shows the operator exactly which node
// failed and why, and only re-touches the broken node.
//
// 2026-08-25 (B180): the pre-B180 handler returned
// `Content-Type: application/json` to the browser. The
// per-row button is a regular `<form method="post">` (see
// admin/exit_nodes.html:241), so the browser treated the
// JSON response as a literal text file and rendered it as
// "Качественная печать" (raw printout page) instead of
// returning the operator to /admin/exit-nodes. The global
// "Sync all" button (line 60) keeps its JSON response
// because it goes through JavaScript `fetch()` + manual
// `location.reload()` (line 376) — the browser's fetch
// pipeline handles JSON fine. The per-row button doesn't
// have JS, so the handler now redirects like every other
// admin POST in this file.
//
// Admin-only. Wire-up is in cmd/skygate/main.go.
func (s *Service) PostAdminExitNodeSync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.SyncRoutesForNode == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("sync not wired (B132)"), http.StatusSeeOther)
		return
	}
	// Path variable via Go 1.22+ mux syntax. The {hostname}
	// is URL-decoded by the mux.
	hostname := r.PathValue("hostname")
	if hostname == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("hostname path var is empty"), http.StatusSeeOther)
		return
	}
	result := s.SyncRoutesForNode(hostname)
	s.Backend.Audit(c.UserID, c.Username, "exit_node_sync_one",
		fmt.Sprintf("hostname=%s result=%v", hostname, result))
	// B180: result is map[string]string with one of these shapes:
	//   {"<hostname>": "ssh=ok approved=34"}            — success
	//   {"<hostname>": "info=no IP/subnet rules..."}   — empty (no rules)
	//   {"error":      "..."}                            — failure
	// Surface the result as a flash message on the page so the
	// operator sees the same content in the toast that the
	// per-row form was missing before B180.
	if errMsg, ok := result["error"]; ok {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("sync "+hostname+": "+errMsg), http.StatusSeeOther)
		return
	}
	msg, ok := result[hostname]
	if !ok {
		msg = fmt.Sprintf("%v", result)
	}
	// B292 (2026-09-23): a failed SSH sync must not render as success. The
	// result string deliberately keeps BOTH halves ("ssh=err=… approved=21" —
	// see syncOneExitNode), because the headscale approve step really did run;
	// but this handler used to redirect with ?ok= unconditionally, so the
	// operator saw a GREEN banner carrying raw `ssh` stderr and read the whole
	// thing as "synced". The ok/err split is now derived from the result.
	if exitSyncFailed(msg) {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Sync "+hostname+": "+msg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Sync "+hostname+": "+msg), http.StatusSeeOther)
}

// exitSyncFailed reports whether a per-node sync result describes a failure.
//
// The result grammar is produced by syncOneExitNode and is grepped by operators,
// so it is parsed rather than changed: "ssh=err=" is an SSH failure,
// "local=err=" (B293 — the relay IS this host) a local apply failure,
// "approve=err=" a headscale failure, and a bare "error=…" the shape the
// /admin/exit-rules JSON endpoint uses. "ssh=ok approved=0" / "local=ok via …
// approved=0" (no routes approved yet) is NOT a failure — it is the normal state
// of a relay whose routes the operator has not approved.
func exitSyncFailed(msg string) bool {
	return strings.Contains(msg, "ssh=err=") ||
		strings.Contains(msg, "local=err=") ||
		strings.Contains(msg, "approve=err=") ||
		strings.HasPrefix(strings.TrimSpace(msg), "error=")
}

// PostAdminExitNodeSetAcceptRoutes is the v1.4.0 B140 per-row
// "accept_routes" toggle on /admin/exit-nodes. The pre-B140
// admin UI only let the operator set this value at initial
// node add (the "Add exit node" form), not edit it per-row
// afterwards — so changing accept_routes for an existing node
// required either a full re-add (which clobbered every other
// field) or direct SQL. The B140 button lets the operator
// cycle 1 (true) / -1 (false) / 0 (default) per-row.
//
// state is read from the form value "state" (integer string
// from the <select>). The handler validates the value before
// hitting the DB; the db.SetExitServerAcceptRoutes helper
// also validates as defense-in-depth.
//
// URL: POST /admin/exit-nodes/{nodeID}/accept-routes
//
// Admin-only. Wire-up is in cmd/skygate/main.go.
func (s *Service) PostAdminExitNodeSetAcceptRoutes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id path var is empty"), http.StatusSeeOther)
		return
	}
	state, err := parseAcceptRoutesFormValue(r.FormValue("state"))
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("accept_routes: "+err.Error()), http.StatusSeeOther)
		return
	}
	// Read the hostname for the audit log. We do this BEFORE the
	// UPDATE so the audit message has the human-readable hostname
	// (the post-update scan would still see it, but the existence
	// check is implicit in UPDATE…WHERE — a 0-rows-affected means
	// the row was deleted between the read and the write).
	hostname, _ := db.GetExitServerHostname(s.dbc(), nodeID)
	if err := db.SetExitServerAcceptRoutes(s.dbc(), nodeID, state); err != nil {
		if errors.Is(err, db.ErrExitServerNotFound) {
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("exit node not found: "+nodeID), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_set_accept_routes",
		fmt.Sprintf("node=%s hostname=%s state=%d", nodeID, hostname, state))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("accept_routes updated for "+nodeID), http.StatusSeeOther)
}
