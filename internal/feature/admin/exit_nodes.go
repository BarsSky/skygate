// Package admin — exit_nodes.go owns the /admin/exit-nodes page
// (list, add, delete, sync, health-now, tag/untag) and the
// helpers used by the headscale-update-monitor banner that
// the template renders above the table.
//
// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved from
// internal/handlers/admin_exit_nodes.go. The handlers used
// to be methods on *App; they now live on *Service. Fields
// that were on *App (SSHKeyPath, ExitNodeMonitor) and the
// SyncAdvertisedRoutes callback are now Service fields,
// wired from cmd/skygate/main.go. The tag-test file
// (admin_exit_nodes_tag_test.go) was deleted because it
// depended on internal/handlers test helpers (authedReqFor,
// newTestApp) that don't exist in this package yet.

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
)

// ExitNodeInfo is the row shape for /admin/exit-nodes. Most
// fields are populated from the DB (db.ListExitServers) and
// enriched from headscale (ListAllNodes) + the health-monitor
// snapshot (db.ListExitNodeHealth). The template renders every
// field — see internal/handlers/templates/admin/exit_nodes.html.
type ExitNodeInfo struct {
	NodeID      string `json:"node_id"`
	Hostname    string `json:"hostname"`
	TailscaleIP string `json:"tailscale_ip"`
	SSHTarget   string `json:"ssh_target"`
	// 2026-08-09 v0.33.1.29 B81: the SSH target that the next
	// SyncAdvertisedRoutes call will actually use, after applying
	// the operator-override → root@<tailscale_ip> → "" fallback
	// chain (via db.LookupExitServerSSHTarget). The template
	// renders this in the SSH column so the operator can see what
	// the SSH step will hit BEFORE the next sync (the previous
	// "show only the stored ssh_target, see the actual one in the
	// audit log after a failed sync" UX was the v0.33.1 trap).
	// SSHTargetAuto is true when the resolved value came from the
	// tailscale_ip column (not from ssh_target) — the template
	// uses it to render a subtle "auto" badge so the operator
	// knows the row is using the B81 fallback and the stored
	// ssh_target column is empty.
	SSHTargetAuto     bool   `json:"ssh_target_auto"`
	ResolvedSSHTarget string `json:"resolved_ssh_target"`
	SSHKeyPath        string `json:"ssh_key_path"`
	// 2026-08-10 v0.33.1.33 B85: the per-row non-default SSH
	// port. The B81 auto-fallback in LookupExitServerSSHTarget
	// appends ":<port>" to "root@<tailscale_ip>" when this is
	// set. Empty = port 22 (the v0.33.1.29 / v0.33.1.32 default).
	// The template renders this in the add form (the only
	// place the operator edits it) — the table render shows
	// the full ResolvedSSHTarget (which already includes the
	// port via the B85 chain).
	SSHPort      string   `json:"ssh_port"`
	Enabled      bool     `json:"enabled"`
	Routes       []string `json:"routes"`
	RouteCount   int      `json:"route_count"`
	SyncStatus   string   `json:"sync_status"`
	Description  string   `json:"description"`
	AcceptRoutes int      `json:"accept_routes"` // -1=false, 0=unset, 1=true
	// 2026-07-15: v0.13.0 — health monitor fields. Populated
	// from exit_node_health (the snapshot table updated by
	// the background monitor) and matched on NodeID. Empty
	// strings / false mean "no snapshot yet" — the page
	// renders a "—" placeholder.
	Online             bool      `json:"online"`
	LastSeen           string    `json:"last_seen"`
	LastSeenAgo        string    `json:"last_seen_ago"`
	State              string    `json:"state"`
	Healthy            bool      `json:"healthy"`
	LastCheckAt        time.Time `json:"last_check_at"`
	HasExitTag         bool      `json:"has_exit_tag"`
	AdvertisedRoutesOK bool      `json:"advertised_routes_ok"`
	// 2026-07-17: v0.18.1 — raw headscale-side state. The
	// "Tag as exit-node" / "Untag" buttons need to know
	// whether the node already has tag:exit-node and
	// whether it advertises 0.0.0.0/0 + ::/0 (the
	// exit-node bases). Without these the template
	// can't decide which button to render.
	Tags                []string `json:"tags"`
	AdvertisesV4Default bool     `json:"advertises_v4_default"`
	AdvertisesV6Default bool     `json:"advertises_v6_default"`
	// B273 (v1.5.18) — the APPROVED half of the same question,
	// read from the live headscale node (NodeView.ApprovedRoutes)
	// rather than from the health snapshot. Advertised-but-
	// unapproved is the single most common "the relay is up but
	// nothing routes" state, and pre-B273 no page showed it:
	// /admin/exit-nodes counted AvailableRoutes (advertised) and
	// the monitor called it healthy. ApprovedV4Default is the one
	// that decides whether clients actually receive the
	// 0.0.0.0/0 exit route.
	ApprovedV4Default bool `json:"approved_v4_default"`
	ApprovedV6Default bool `json:"approved_v6_default"`
	// ApprovedRoutesOK is the single-boolean form used by the
	// template's "маршруты не одобрены" warning tag.
	ApprovedRoutesOK bool `json:"approved_routes_ok"`
	// B292 (2026-09-23) — the SSH half of the same "will the next sync be able
	// to do anything" question.
	//
	// EffectiveSSHKeyPath is the path the next sync will pass to `ssh -i`: the
	// row's ssh_key_path, else the global default (which is now resolved per
	// install kind — /ssh-sync/id_ed25519 inside the container, a host path under
	// the data dir on a native install). SSHKeyState is one of
	// ok/unset/missing/unreadable/directory/relative/inaccessible (see
	// headscale.SSHKeyState) and SSHKeyNote carries the same reason plus the fix
	// for the tooltip. Pre-B292 none of this was on the page: the first sign of
	// trouble was a raw `ssh` warning inside a GREEN flash after pressing
	// Re-sync, while the sync silently advertised nothing.
	EffectiveSSHKeyPath string `json:"effective_ssh_key_path"`
	SSHKeyState         string `json:"ssh_key_state"`
	SSHKeyNote          string `json:"ssh_key_note"`
	// B293 (2026-09-23) — this relay IS this host.
	//
	// LocalRelay is set when the live local tailscaled's own addresses include
	// one of the relay's headscale addresses (the operator's `aro`: the local
	// daemon is `exit-node-vps`, 100.64.0.1). Such a relay is configured with a
	// LOCAL `tailscale set` (see internal/headscale/local_apply_b293.go) — no
	// SSH, no key — so the SSH-column warning is suppressed and the row says so.
	// LocalTransport names the rung the next sync will use (direct / sudo /
	// helper), or why none is available.
	LocalRelay     bool   `json:"local_relay"`
	LocalTransport string `json:"local_transport"`
}

// AdminExitNodes renders the /admin/exit-nodes page. Admin-only.
func (s *Service) AdminExitNodes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// 2026-07-31: v0.32.13 — call ensureExitServers inside
	// a 2s timeout goroutine. The first call after
	// container start hangs the SQLite WAL write lock
	// (clean-up DELETE loop in v0.32.7's
	// ensureExitServers) for 10-30s. We don't want the
	// /admin/exit-nodes page to hang for that long. If
	// the timeout fires we just render the page from the
	// current exit_servers rows in the DB; the page is
	// still useful (the discovery just enriches it).
	esDone := make(chan struct{})
	go func() {
		s.ensureExitServers()
		close(esDone)
	}()
	select {
	case <-esDone:
	case <-time.After(2 * time.Second):
		log.Printf("[exit-nodes] ensureExitServers TIMEOUT after 2s, continuing without discovery")
	}
	// B276: the assignment table + the live-policy comparison. Loaded once here so
	// the page, the drift counters and the ACL staleness banner all describe the
	// same snapshot.
	//
	// B277: and grouped (by owner / domain / device) with bulk actions and a global
	// override, which is what makes the table usable once the dead rows are pruned.
	prefixRows, prefixStats := s.loadPrefixOwnerRows()
	prefixAdmin := s.loadPrefixAdminView(r.URL.Query().Get("group"), r.URL.Query().Get("only") == "drift")

	// 2026-07-12: Этап 10 part 5 — moved to db.ListExitServers.
	// 2026-07-31: v0.32.13 — wrap in 2s timeout. Even
	// after the ensureExitServers timeout, the followup
	// db.ListExitServers SELECT can still hang on the WAL
	// write lock (busy_timeout=5s × 2 = up to 10s). We
	// render an empty nodes slice on timeout rather than
	// hang the page.
	listDone := make(chan struct{})
	var dbRows []db.ExitServer
	var listErr error
	go func() {
		dbRows, listErr = db.ListExitServers(s.dbc())
		close(listDone)
	}()
	select {
	case <-listDone:
	case <-time.After(2 * time.Second):
		log.Printf("[exit-nodes] db.ListExitServers TIMEOUT after 2s, rendering empty")
		listErr = fmt.Errorf("timeout")
	}
	// 2026-09-18 (R6): pre-fix this was
	//     http.Error(w, listErr.Error(), http.StatusInternalServerError)
	// which REPLACED the entire /admin/exit-nodes page with a text/plain
	// body containing the raw SQL error — the operator's "the DB error is
	// shown as a separate page" report. The template already renders
	// .FlashError, so surface the failure there and render the (empty)
	// table around it. The detailed error still goes to the log.
	var listErrMsg string
	if listErr != nil {
		log.Printf("[exit-nodes] db.ListExitServers failed: %v", listErr)
		listErrMsg = s.I18n.T(s.I18n.LangFromRequest(r), "error.db")
	}

	var nodes []ExitNodeInfo
	for _, e := range dbRows {
		n := ExitNodeInfo{
			NodeID:      e.NodeID,
			Hostname:    e.Hostname,
			TailscaleIP: e.TailscaleIP,
			SSHTarget:   e.SSHTarget,
			SSHKeyPath:  e.SSHKeyPath,
			// v0.33.1.33 B85: the per-row non-default SSH port.
			// Empty = port 22 (the v0.33.1.29 default; the B81
			// auto-fallback then produces "root@<tailscale_ip>"
			// with no port suffix). Non-empty = the operator
			// has set a custom port (e.g. 18022 for karolina);
			// the B85 fallback appends it.
			SSHPort:      e.SSHPort,
			Enabled:      e.Enabled,
			Description:  e.Description,
			AcceptRoutes: e.AcceptRoutes,
		}
		// 2026-08-09 v0.33.1.29 B81: pre-resolve the SSH target
		// the template will render, so the operator sees the same
		// value SyncAdvertisedRoutes will use on the next tick
		// (previously the table only showed the stored ssh_target
		// and the actual SSH call fell back to nodeHostname when
		// ssh_target was empty — making it impossible to predict
		// which host the SSH would hit until the next sync
		// failed). The resolved target is also used by the
		// "Use Tailscale IP" button (which becomes visible when
		// the stored ssh_target differs from the resolved one).
		if resolved, lerr := db.LookupExitServerSSHTarget(s.dbc(), e.Hostname); lerr == nil {
			n.ResolvedSSHTarget = resolved
			n.SSHTargetAuto = strings.TrimSpace(e.SSHTarget) == "" && resolved != ""
		}
		// B292: what the NEXT sync will pass to `ssh -i`. The row's own
		// ssh_key_path wins; otherwise the global default, which is resolved per
		// install kind (see config.resolveExitSSHKeyPath). The state + note are
		// pure filesystem probes, so an unusable key is visible on the page
		// instead of only in the stderr of a failed ssh.
		effectiveKey := s.effectiveExitSSHKeyPath(e.SSHKeyPath)
		n.EffectiveSSHKeyPath = effectiveKey
		n.SSHKeyState = headscale.SSHKeyState(effectiveKey)
		if problem := headscale.SSHKeyProblem(effectiveKey); problem != "" {
			n.SSHKeyNote = problem + " — " + headscale.SSHKeyFixHint(effectiveKey)
		}
		nodes = append(nodes, n)
	}

	// B293: ask who this machine is, then mark the relay(s) it owns. The probe
	// prefers the live daemon and falls back to the local interface addresses
	// (B293.1) — the native install runs skygate as an unprivileged service user,
	// and tailscaled's socket is root-owned unless the operator granted
	// `--operator`, so requiring the daemon would leave exactly this page unable to
	// recognise the local relay. One probe for the whole page, not per row.
	selfIPs := headscale.LocalSelfIPs()
	if len(selfIPs) > 0 {
		transportName := "unavailable — " + headscale.RoutesFallbackHint()
		if trs := headscale.LocalTransports(); len(trs) > 0 {
			transportName = trs[0].Name + " (" + trs[0].Detail + ")"
		}
		for i := range nodes {
			if ip, ok := headscale.IsLocalRelay(headscale.LocalSelf{IPs: selfIPs}, splitCommaList(nodes[i].TailscaleIP)); ok {
				nodes[i].LocalRelay = true
				nodes[i].LocalTransport = transportName
				// The SSH key is irrelevant for this row: the sync applies
				// locally. Suppressing it here is what keeps the B292 warning
				// honest instead of permanent noise.
				nodes[i].SSHKeyState = "ok"
				nodes[i].SSHKeyNote = ""
				log.Printf("[exit-nodes] %s IS this host (matched %s) — routes are applied locally via %s, SSH is not used", nodes[i].Hostname, ip, transportName)
			}
		}
	} else {
		log.Printf("[exit-nodes] cannot determine this host's own addresses (neither the local tailscaled nor the interface list answered) — every relay keeps the SSH transport")
	}

	// 2026-07-31: v0.32.13 — same 2s timeout on the second
	// ListAllNodes() call. The first call (in
	// ensureExitServers) is wrapped above; this one too
	// because the cacheTTL is 5s and the second call may
	// be a cache miss (e.g. the first hung and didn't
	// populate the cache).
	hsDone := make(chan struct{})
	var hsNodes []headscale.NodeView
	var hsErr error
	go func() {
		hsNodes, hsErr = s.HSGlobalFn().ListAllNodes()
		close(hsDone)
	}()
	select {
	case <-hsDone:
	case <-time.After(2 * time.Second):
		log.Printf("[exit-nodes] ListAllNodes (enrich) TIMEOUT after 2s, rendering without headscale enrichment")
		hsErr = fmt.Errorf("timeout")
	}
	hsEnriched := hsErr == nil && hsNodes != nil
	if hsEnriched {
		for i := range nodes {
			for _, hn := range hsNodes {
				nid, _ := strconv.Atoi(nodes[i].NodeID)
				hnID, _ := strconv.Atoi(hn.ID)
				if nid == hnID {
					if nodes[i].TailscaleIP == "" && len(hn.IPAddresses) > 0 {
						nodes[i].TailscaleIP = hn.IPAddresses[0]
					}
					nodes[i].Routes = hn.AvailableRoutes
					nodes[i].RouteCount = len(hn.AvailableRoutes)
					// 2026-07-17: v0.18.1 — surface the
					// raw headscale tags + exit-node-base
					// advertising state so the template
					// can render the "Tag as exit-node"
					// / "Untag" buttons correctly.
					nodes[i].Tags = hn.Tags
					for _, r := range hn.AvailableRoutes {
						if r == "0.0.0.0/0" {
							nodes[i].AdvertisesV4Default = true
						}
						if r == "::/0" {
							nodes[i].AdvertisesV6Default = true
						}
					}
					// B273 (v1.5.18) — the approved half.
					for _, r := range hn.ApprovedRoutes {
						if r == "0.0.0.0/0" {
							nodes[i].ApprovedV4Default = true
						}
						if r == "::/0" {
							nodes[i].ApprovedV6Default = true
						}
					}
					nodes[i].ApprovedRoutesOK = nodes[i].ApprovedV4Default
					if nodes[i].Hostname == "" {
						nodes[i].Hostname = hn.GivenName
					}
					break
				}
			}
		}
	}

	ruleRows, _ := s.dbc().Query("SELECT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet')")
	if ruleRows != nil {
		defer ruleRows.Close()
		expectedRoutes := map[string]int{}
		for ruleRows.Next() {
			var node, target string
			if ruleRows.Scan(&node, &target) == nil {
				expectedRoutes[node]++
			}
		}
		// 2026-07-30: extracted the SyncStatus calculation
		// into computeSyncStatus() so it can be unit-tested
		// without spinning up a headscale mock. The function
		// is the SAME logic as the inline check that was here
		// before — just hoisted out for testability.
		for i := range nodes {
			nodes[i].SyncStatus = computeSyncStatus(nodes[i].Hostname, nodes[i].RouteCount, expectedRoutes)
		}
	}

	// 2026-07-15: v0.13.0 — overlay the health-monitor
	// snapshot on each row (matched by node_id). The snapshot
	// may not exist yet (monitor hasn't ticked, or this node
	// was added after the last tick); the template renders
	// "—" placeholders in that case.
	healthRows, _ := db.ListExitNodeHealth(s.dbc())
	healthByID := make(map[string]db.ExitNodeHealth, len(healthRows))
	now := time.Now().UTC()
	for _, h := range healthRows {
		healthByID[h.NodeID] = h
	}
	healthyCount := 0
	// B273 (v1.5.18): the two "works, but…" buckets the page now
	// names explicitly instead of folding them into the red
	// "Нет рабочих exit-узлов!" banner.
	untaggedCount := 0
	unapprovedCount := 0
	// scoredCount is how many rows actually carry a verdict (a
	// health snapshot, or a live-derived state). The red
	// zero-healthy banner is gated on it: a row in exit_servers
	// whose node headscale did not return (deleted node, headscale
	// unreachable) has no verdict, and "we do not know" must not be
	// rendered as "everything is down" — that class of false alarm
	// is what B273 is about.
	scoredCount := 0
	for i := range nodes {
		h, ok := healthByID[nodes[i].NodeID]
		if ok {
			nodes[i].Online = h.Online
			nodes[i].LastSeen = h.LastSeen
			nodes[i].State = h.State
			nodes[i].Healthy = h.Healthy
			nodes[i].LastCheckAt = h.LastCheckAt
			nodes[i].HasExitTag = h.HasExitTag
			nodes[i].AdvertisedRoutesOK = h.AdvertisedRoutesOK
			if !h.LastSeenParsed.IsZero() {
				nodes[i].LastSeenAgo = humanizeDuration(now.Sub(h.LastSeenParsed))
			}
		}
		// The counts are derived from the LIVE headscale view where
		// it is available, because the snapshot can be up to
		// CheckEvery (5 min) old and the operator is looking at this
		// page precisely to find out what is wrong right now. The
		// snapshot remains the fallback for a row this page could not
		// enrich (headscale timeout above) — in that case Tags and
		// ApprovedRoutesOK are both zero, so the switch is skipped
		// rather than inventing an "untagged" verdict from missing
		// data.
		if hsEnriched {
			switch {
			case nodes[i].ApprovedRoutesOK && !hasExitNodeTagFor(nodes[i].Tags):
				untaggedCount++
				nodes[i].State = "untagged"
				nodes[i].Healthy = true
			case nodes[i].AdvertisesV4Default && !nodes[i].ApprovedRoutesOK:
				unapprovedCount++
				if nodes[i].State == "" || nodes[i].State == "online" || nodes[i].State == "untagged" {
					nodes[i].State = "degraded"
					nodes[i].Healthy = false
				}
			}
		}
		if nodes[i].Healthy {
			healthyCount++
		}
		if nodes[i].State != "" {
			scoredCount++
		}
	}

	// B292: how many relays the NEXT advertised-routes sync cannot reach because
	// the SSH key is missing/unreadable. One relay is enough to make the whole
	// "Sync" button a no-op for that node, and pre-B292 the page said nothing at
	// all — the operator learned it from the stderr inside a green flash.
	sshKeyBlocked := 0
	var sshKeyBlockedNote string
	for i := range nodes {
		if headscale.SSHKeyStateNeedsOperator(nodes[i].SSHKeyState) {
			sshKeyBlocked++
			if sshKeyBlockedNote == "" {
				sshKeyBlockedNote = nodes[i].SSHKeyNote
			}
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/exit_nodes.html", c, map[string]any{
		"Page":         "exit-nodes",
		"Title":        "Exit Nodes",
		"Nodes":        nodes,
		"SSHKeyPath":   s.SSHKeyPath,
		"HealthyCount": healthyCount,
		"TotalCount":   len(nodes),
		// B292: the SSH-key half of "will the next sync do anything".
		"SSHKeyBlocked":     sshKeyBlocked,
		"SSHKeyBlockedNote": sshKeyBlockedNote,
		// B273 (v1.5.18): "works, but the exit-node tag is missing"
		// and "up, but the route was never approved" are two
		// DIFFERENT operator problems with two different fixes. The
		// page names each one instead of answering both with the red
		// "Нет рабочих exit-узлов!" banner.
		"UntaggedCount":   untaggedCount,
		"UnapprovedCount": unapprovedCount,
		"ScoredCount":     scoredCount,
		// B275.1: the prefix assignment table. headscale serves a subnet
		// prefix from exactly one relay, so "which relay advertises this
		// prefix" is a first-class, operator-editable decision — not a
		// side effect of which device's rule happened to sync last.
		//
		// B276: the view returns the drifted/pinned rows (not all ~1500) plus the
		// summary the page needs to say whether the LIVE policy still pins the old
		// relay — the failure that used to be invisible.
		"PrefixRows":     prefixRows,
		"PrefixStats":    prefixStats,
		"PrefixAdmin":    prefixAdmin,
		"RelayChoices":   relayChoicesFor(nodes),
		"MonitorRunning": s.ExitNodeMonitor != nil,
		"FlashSuccess":   r.URL.Query().Get("ok"),
		"FlashError":     firstNonEmptyStr(r.URL.Query().Get("err"), listErrMsg), // 2026-07-20: v0.20.0 — headscale-update-monitor
		// B266 (2026-09-19): the one-time pre-auth key + ready-to-run
		// command from "Зарегистрировать новый exit node". The key is
		// parked in global_settings under an opaque token (never in the
		// URL) and consumed here, so it renders exactly once.
		"RegisterKey": s.consumeExitNodeRegisterKey(r.URL.Query().Get("registered")),
		"ControlURL":  s.controlURL(),
		// banner. The template renders a coloured
		// "newer headscale available" hint above the
		// exit-node table when a release newer than the
		// operator's pinned version is known. nil-safe:
		// the template guards with `if .HeadscaleUpdate`.
		"HeadscaleUpdate":   headscaleUpdateForBanner(s),
		"HeadscaleBreaking": headscaleBreakingForBanner(s),
		"HeadscaleLatest":   headscaleLatestTag(s),
		"HeadscalePinned":   headscalePinnedTag(s),
		"HeadscaleHTMLURL":  headscaleHTMLURL(s),
	})
}

// splitCommaList splits the comma-joined tailscale_ip column into its entries.
// Pure; used by the B293 co-location check (a relay's address list can carry IPv4
// and IPv6, and the local daemon may own either).
func splitCommaList(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// effectiveExitSSHKeyPath returns the SSH private key the next advertised-routes
// sync will use for a relay whose exit_servers.ssh_key_path is `rowKeyPath`
// (B292).
//
// The row's own value wins; otherwise the global default from Config
// (SKYGATE_EXIT_SSH_KEY, or the install-kind default — see
// config.resolveExitSSHKeyPath). Pure, so the page can be tested without a
// filesystem.
func (s *Service) effectiveExitSSHKeyPath(rowKeyPath string) string {
	if p := strings.TrimSpace(rowKeyPath); p != "" {
		return p
	}
	if s != nil && s.Cfg != nil {
		return strings.TrimSpace(s.Cfg.SSHKeyPath)
	}
	return ""
}

// hasExitNodeTagFor reports whether the headscale tag list carries
// tag:exit-node. B273 (v1.5.18) — the comparison is
// case-insensitive so it agrees with headscale's own
// hasExitNodeTag (which uses strings.EqualFold) and with the health
// monitor; the pre-B273 exact `== "tag:exit-node"` test meant a node
// tagged `Tag:Exit-Node` was "tagged" for routing and "untagged" for
// the health banner.
func hasExitNodeTagFor(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, "tag:exit-node") {
			return true
		}
	}
	return false
}

// computeSyncStatus is the pure helper that decides
// whether an exit node's advertised-routes count from
// headscale matches the count of device_rules in skygate
// that target that node.
//
// 2026-07-30: v0.32.3 — extracted from the inline loop
// in AdminExitNodes so the contract is unit-testable
// (see exit_nodes_test.go). The function is small and
// has no side effects; the integration between
// computeSyncStatus + the headscale-fetching code path
// is covered by the live verify-post checks.
//
// Returns one of:
//
//	""                            — no rules target this node, no status
//	"synced"                      — skygate rules count == headscale routes
//	"mismatch: have N, want M"    — drift detected
//
// "have N" is the headscale-side count (len(AvailableRoutes))
// and "want M" is the skygate-side count (device_rules
// with exit_node_id == hostname). When "want M" is 0 the
// status stays empty (the node is not in use from skygate's
// view; headscale may still have routes from the operator's
// manual setup, and that's fine).
//
// The "mismatch" wording is preserved verbatim — the
// /admin/exit-nodes page renders this string in the
// "СТАТУС" column and operators have come to expect it.
func computeSyncStatus(hostname string, routeCount int, expectedRoutes map[string]int) string {
	expected := expectedRoutes[hostname]
	if expected > 0 && routeCount != expected {
		return fmt.Sprintf("mismatch: have %d, want %d", routeCount, expected)
	}
	if expected > 0 {
		return "synced"
	}
	return ""
}

// headscaleUpdateForBanner is a small helper that
// returns the headscale-update-monitor's
// UpdateAvailable flag (or false if the monitor is
// not wired). Keeping the helper separate from
// the data map means the template can use it as a
// single condition without nil-checks inline.
//
// v0.20.0. 2026-07-20.
func headscaleUpdateForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, upd, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return upd
}

// headscaleBreakingForBanner returns the
// BreakingAvailable flag (same nil-safe pattern).
func headscaleBreakingForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, _, brk, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return brk
}

// headscaleLatestTag returns the latest seen release
// tag (or "" if the monitor is not wired / hasn't
// polled yet).
func headscaleLatestTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.TagName
}

// headscalePinnedTag returns the operator's pinned
// version (or "").
func headscalePinnedTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	_, _, _, _, _, pinned := s.HeadscaleUpdateMonitor.Snapshot()
	return pinned
}

// headscaleHTMLURL returns the GitHub release URL
// for the latest seen release (or "").
func headscaleHTMLURL(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.HTMLURL
}

// humanizeDuration formats a time.Duration as a short
// human-readable string ("3s", "2m 14s", "1h 5m", "2d 3h").
// Used by /admin/exit-nodes to render the "last seen X ago"
// column without pulling moment.js / dayjs. Negative inputs
// are treated as "0s" (the monitor's clock skew can produce
// these on a clock-adjusting laptop).
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

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

// relayChoicesFor returns the distinct relay hostnames shown in the
// prefix-assignment select (the exit_servers rows the page already rendered,
// de-duplicated and sorted). B275.1.
func relayChoicesFor(nodes []ExitNodeInfo) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range nodes {
		h := strings.TrimSpace(n.Hostname)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// PrefixOwnerRow is one row of the /admin/exit-nodes
// "prefix assignment" table (B275.1).
//
// The table is the authority for WHICH relay advertises a prefix: headscale
// serves a subnet prefix from exactly one relay, so the per-rule exit node can
// be honoured for only one of two devices that want the same CIDR. The engine
// (internal/prefixowner) picks the owner — explicit rules win, the rest is spread
// over the healthy relays and is sticky — and the operator can pin a prefix by
// hand here (source='manual', which the engine never overwrites while that relay
// is healthy).
type PrefixOwnerRow struct {
	Prefix   string
	ExitNode string
	// Source is explicit | manual | auto — why this relay owns the prefix.
	Source  string
	Claims  int
	Devices int
	// Advertised is true when the owning relay currently reports the prefix in
	// its available routes; Assigned-but-not-advertised is the state the
	// operator has to look at (the relay's route sync has not caught up yet, or
	// the SSH sync failed).
	Advertised bool
	// B276: AlsoBy lists the OTHER relays that advertise the same prefix. Two
	// advertisers is the B274 state — headscale picks one primary and can move it
	// between passes, so the pin matches only half the time.
	AlsoBy []string
	// B276: Unserved is true when NO relay advertises the prefix: the rule is
	// dead weight (the ACL may grant it, but no route exists to carry it).
	Unserved bool
}

// PrefixDriftStats is the B276 "why is this prefix not working" summary the exit
// nodes page renders. It answers the three questions that were previously only
// answerable by diffing SQL against headscale by hand:
//
//  1. does the owning relay actually advertise its prefixes (NotAdvertised /
//     Unserved / Duplicated);
//  2. is the policy headscale serves the one skygate would generate right now
//     (PolicyInSync) — the stale-ACL class that silently killed every pin for a
//     prefix when the assignment moved to another relay;
//  3. which rows the page is showing out of the whole table (Shown/Total).
type PrefixDriftStats struct {
	Total          int
	Shown          int
	NotAdvertised  int
	Unserved       int
	Duplicated     int
	PinnedDrifted  int
	PolicyChecked  bool
	PolicyInSync   bool
	PolicyErr      string
	PolicyBytes    int
	PolicyLiveByte int
	// PolicyDetail names which sections of the two documents differ (B288), so
	// the banner cannot blame the `via` pins for a difference that is, say, two
	// extra tag declarations. Empty when the policies are equivalent.
	PolicyDetail string
	// PolicyApply is what the privileged applier recorded the last time it ran
	// (B288.1: "ok @ 21:47, 5082 bytes" or "failed — headscale did not answer on
	// …"). It is the missing half of the drift story: a stale policy whose
	// applier says OK means the write happened but headscale serves something
	// else; an applier that says failed names its reason outright.
	PolicyApply string
}

// prefixDriftRowLimit caps how many rows the page renders. The assignment table
// holds every prefix the rules ever produced (live: ~1500 rows), and rendering all
// of them with a relay <select> per row made the page megabytes of HTML for no
// benefit — the operator needs the drifted rows, not the healthy ones.
const prefixDriftRowLimit = 200

// loadPrefixOwnerRows reads the assignment table, resolves what each relay
// advertises and what headscale serves, and returns the rows worth looking at plus
// the summary the page renders (B276: the rows are the drifted and manually pinned
// ones, not all ~1500 table entries, and the summary says whether the LIVE policy
// still matches the table).
//
// Signature note (B276): this used to return just the rows (B275.1). The page needs
// both halves from ONE headscale read, so the contract in
// scripts/check_b275_1_prefix_ui.sh was renegotiated to the two-value form — the
// property it protects (the page reads the assignment table and hands rows to the
// template) is unchanged.
func (s *Service) loadPrefixOwnerRows() ([]PrefixOwnerRow, PrefixDriftStats) {
	var stats PrefixDriftStats
	rows, err := s.dbc().Query(`SELECT prefix, exit_node_id, COALESCE(source,'auto'),
	                                   COALESCE(claims,0), COALESCE(devices,0)
	                            FROM prefix_owner ORDER BY prefix`)
	if err != nil {
		log.Printf("[exit-nodes] prefix_owner read failed: %v", err)
		return nil, stats
	}
	defer rows.Close()
	// advertised[relay] = set of prefixes the relay currently advertises, and
	// advertisers[prefix] = the relays advertising it, so both "my owner is silent"
	// and "two relays claim it" are visible from one headscale read.
	advertised := map[string]map[string]bool{}
	advertisers := map[string][]string{}
	if hsNodes, herr := s.HSGlobalFn().ListAllNodes(); herr == nil {
		for _, n := range hsNodes {
			set := map[string]bool{}
			for _, p := range n.AvailableRoutes {
				set[p] = true
				advertisers[p] = append(advertisers[p], strings.ToLower(n.Hostname))
			}
			advertised[strings.ToLower(n.Hostname)] = set
		}
	}
	var all []PrefixOwnerRow
	for rows.Next() {
		var r PrefixOwnerRow
		if err := rows.Scan(&r.Prefix, &r.ExitNode, &r.Source, &r.Claims, &r.Devices); err != nil {
			continue
		}
		if set := advertised[strings.ToLower(r.ExitNode)]; set != nil {
			r.Advertised = set[r.Prefix]
		}
		owner := strings.ToLower(r.ExitNode)
		for _, a := range advertisers[r.Prefix] {
			if a != owner {
				r.AlsoBy = append(r.AlsoBy, a)
			}
		}
		r.Unserved = len(advertisers[r.Prefix]) == 0
		all = append(all, r)
	}
	stats.Total = len(all)
	// B277 RENEGOTIATION of the B276 row filter: the loader used to hand the template
	// only the drifted and manually pinned rows, because the table held every prefix it
	// had ever seen (live: 1655 rows, 1497 dead) and printing them all produced
	// megabytes of HTML. The engine now prunes the dead rows (prefixowner.Prune), so
	// the table describes the network (~150 rows) and the operator needs ALL active
	// prefixes on screen — a group is pinned as a group, and a healthy Cloudflare row
	// that is hidden cannot be pinned. The drift filter moved to the page
	// (`?only=drift`, loadPrefixAdminView).
	out := make([]PrefixOwnerRow, 0, len(all))
	for _, r := range all {
		drifted := !r.Advertised || r.Unserved || len(r.AlsoBy) > 0
		switch {
		case r.Unserved:
			stats.Unserved++
		case !r.Advertised:
			stats.NotAdvertised++
		}
		if len(r.AlsoBy) > 0 {
			stats.Duplicated++
		}
		if r.Source == "manual" && drifted {
			stats.PinnedDrifted++
		}
		if len(out) < prefixDriftRowLimit {
			out = append(out, r)
		}
	}
	stats.Shown = len(out)
	s.fillPolicyDrift(&stats)
	return out, stats
}

// fillPolicyDrift compares the policy headscale is serving with the one skygate
// would generate right now (B276). A mismatch means the per-CIDR `via` pins were
// produced from an older assignment table — the exact silent failure this block
// closes.
func (s *Service) fillPolicyDrift(stats *PrefixDriftStats) {
	gen, err := acl.GenerateACLLiveFormat(s.dbc())
	if err != nil {
		stats.PolicyErr = "generate: " + err.Error()
		return
	}
	stats.PolicyBytes = len(gen)
	hs := s.HSGlobalFn()
	if hs == nil {
		stats.PolicyErr = "no headscale client"
		return
	}
	live, err := hs.GetACL()
	if err != nil {
		stats.PolicyErr = "read live policy: " + err.Error()
		return
	}
	stats.PolicyLiveByte = len(live)
	same, cmpErr := headscale.PolicyEquivalent(gen, live)
	if cmpErr != nil {
		stats.PolicyErr = "compare: " + cmpErr.Error()
		return
	}
	stats.PolicyChecked = true
	stats.PolicyInSync = same
	// B288.1: report what the privileged applier said the last time it ran. A
	// missing file means the applier has never run (or is older than B288.1).
	if st, ok := headscale.ReadPolicyApplyStatus(); ok {
		stats.PolicyApply = st.Summary()
	}
	if !same {
		// B288: say WHAT differs. The banner used to explain every drift with
		// the `via`-pin story; live on `aro` the only differences were 16
		// duplicated grants (semantically no-ops) and two tag declarations, so
		// the explanation pointed at the one thing that was not wrong.
		if detail, dErr := headscale.PolicyDriftDetail(gen, live); dErr == nil {
			stats.PolicyDetail = detail
		}
	}
}

// PostAdminExitPrefixOwner pins one prefix to one relay (B275.1), or hands it
// back to the engine when the relay field is empty. Admin-only, audited.
func (s *Service) PostAdminExitPrefixOwner(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	prefix := strings.TrimSpace(r.FormValue("prefix"))
	relay := strings.TrimSpace(r.FormValue("relay"))
	if prefix == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("prefix is empty"), http.StatusSeeOther)
		return
	}
	if err := prefixowner.SetManual(s.dbc(), prefix, relay); err != nil {
		log.Printf("[exit-nodes] SetManual(%s, %s): %v", prefix, relay, err)
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	action := "prefix_owner_pin"
	if relay == "" {
		action = "prefix_owner_auto"
	}
	s.Backend.Audit(c.UserID, c.Username, action, fmt.Sprintf("prefix=%s relay=%s", prefix, relay))
	// B277: re-apply the ACL right away. prefixowner.Assign keeps a manual row it
	// finds, so the next sync pass reports no CHANGE for a manual pin — without this
	// the pin would sit in the database while headscale kept serving the old relay.
	s.finishPrefixChange(w, r, c.Username, action+" prefix="+prefix+" relay="+relay,
		"назначение сохранено: "+prefix+" → "+relay)
}

// PostAdminExitNodeACLResync regenerates the headscale policy from the current
// database state and pushes it (B276).
//
// This is the operator's escape hatch for the class this block closes: the
// assignment table and the advertised routes are recomputed every few minutes,
// but the ACL is only regenerated when a rule/user/device changes — so after an
// ownership flip the live policy keeps pinning prefixes to the relay that no
// longer serves them, and the clients silently lose those routes. The sync paths
// now re-apply automatically; the button exists because the operator may have just
// moved a prefix by hand (or a sync failed) and should not have to wait for a tick.
//
// Admin-only, audited like every other ACL apply.
func (s *Service) PostAdminExitNodeACLResync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	results := acl.ApplyACLForAllPlanes(s.dbc(),
		func(string) *headscale.Client { return s.HSGlobalFn() },
		alerter,
		c.Username,
		fmt.Sprintf("acl resync from /admin/exit-nodes by %s (prefix ownership pins)", c.Username),
		viaFlag,
	)
	for _, res := range results {
		if res.Err != nil {
			log.Printf("[exit-nodes] ACL resync by %s failed: %v", c.Username, res.Err)
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("ACL re-apply failed: "+res.Err.Error()), http.StatusSeeOther)
			return
		}
	}
	log.Printf("[exit-nodes] ACL resync by %s: %d plane(s) updated", c.Username, len(results))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(fmt.Sprintf("ACL regenerated from the current assignment table (%d plane(s))", len(results))), http.StatusSeeOther)
}

// parseAcceptRoutesFormValue converts the form "state" string
// to the -1/0/1 int the column + headscale SetAdvertisedRoutes
// expect. Returns a friendly error for unknown values so the
// handler can render a "bad value" flash without 500ing.
//
//	"1"   →  1  (true)
//	"0"   →  0  (default / unset)
//	"-1"  → -1  (false)
//
// Whitespace trimmed. Anything else → error.
func parseAcceptRoutesFormValue(s string) (int, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "1":
		return 1, nil
	case "0":
		return 0, nil
	case "-1":
		return -1, nil
	default:
		return 0, fmt.Errorf("state must be 1, 0, or -1 (got %q)", s)
	}
}

// PostAdminExitNodeTagAsExitNode is the v0.18.1 "Tag as
// exit-node" button on /admin/exit-nodes. It replaces the
// operator's two manual `docker exec headscale headscale
// nodes ...` invocations with a single click:
//
//  1. Approves the exit-node bases (0.0.0.0/0, ::/0) on
//     the headscale side via the CLI. We approve ONLY the
//     two base routes, not the full availableRoutes set
//     (relay-3 has 200+ subnets that the operator does
//     NOT want auto-approved).
//  2. Tags the node with `tag:exit-node`. The ACL
//     already includes `* → tag:exit-node:*` so the new
//     node immediately starts accepting tailnet traffic.
//
// Both steps go through the same docker-exec headscale
// CLI that the operator used to run by hand. The handler
// refuses to act if:
//   - the node doesn't have 0.0.0.0/0 AND ::/0 advertised
//     (operator hasn't run `tailscale set --advertise-exit-node` yet)
//   - the node is already tagged with `tag:exit-node`
//     (idempotency: this handler is for the
//     "tag" half of the workflow, not the "untag")
//
// PostAdminExitNodeUntagAsExitNode (below) handles the
// reverse — removing tag:exit-node from a node.
func (s *Service) PostAdminExitNodeTagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := r.FormValue("node_id")
	if idStr == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id required"), http.StatusSeeOther)
		return
	}
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	// Find the node and verify it has the exit-node
	// bases advertised. We refuse to tag a node that
	// hasn't advertised 0.0.0.0/0+::/0 (the operator
	// must run `tailscale set --advertise-exit-node`
	// first — that's the "I want this to be an exit-node"
	// gate). This is also why the button is only rendered
	// in the template for nodes that have these routes
	// advertised; the server-side check is defense in
	// depth in case the operator crafts a POST by hand.
	allNodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("list nodes: "+err.Error()), http.StatusSeeOther)
		return
	}
	var target *headscale.NodeView
	for i := range allNodes {
		if allNodes[i].ID == idStr {
			target = &allNodes[i]
			break
		}
	}
	if target == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node not found"), http.StatusSeeOther)
		return
	}
	hasV4, hasV6 := false, false
	for _, rt := range target.AvailableRoutes {
		if rt == "0.0.0.0/0" {
			hasV4 = true
		}
		if rt == "::/0" {
			hasV6 = true
		}
	}
	if !hasV4 || !hasV6 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"node does not advertise 0.0.0.0/0 + ::/0 yet — run `tailscale set --advertise-exit-node` on the relay first"), http.StatusSeeOther)
		return
	}

	// Idempotency: if the node already has tag:exit-node,
	// skip the TagNode call. The button is hidden in this
	// case but we re-check here.
	for _, t := range target.Tags {
		if t == "tag:exit-node" {
			http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
				fmt.Sprintf("%s is already tagged as exit-node", target.Hostname)), http.StatusSeeOther)
			return
		}
	}

	// Step 1: approve the exit-node bases. We approve
	// ONLY 0.0.0.0/0 and ::/0 (not the full availableRoutes)
	// to avoid accidentally approving relay-3's 200+
	// subnets.
	hs := s.HSGlobalFn()
	approved, err := hs.ApproveRoutesForNodeID(nodeID, []string{"0.0.0.0/0", "::/0"})
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("approve-routes: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Step 2: tag with tag:exit-node. The ACL already
	// allows `* → tag:exit-node:*`, so the node starts
	// accepting traffic immediately on the next ACL
	// poll by the Tailscale client (usually <60s).
	//
	// 2026-08-10 v0.33.1.35 B87: switched from hs.TagNode to
	// hs.AddTag. Pre-fix, TagNode REPLACED the entire tag
	// set on the node (headscale's `nodes tag --force`
	// subcommand takes a full tag set, not a delta). The
	// exit-nodes on the live VM are also per-user devices
	// (tagged `tag:dev-skyadmin-emilia`, etc.) — the
	// v0.33.1.30 B82 follow-up documented that
	// `tag:dev-skyadmin-*` is the per-user device marker
	// that lets the per-user ACL grants resolve. The
	// pre-fix TagNode silently wiped those tags on every
	// "Tag as exit-node" click, breaking the per-user grant
	// until the operator re-applied the tag manually. The
	// fix: AddTag (read-modify-write at
	// internal/headscale/tags.go:117) reads the current
	// tag set first, appends `tag:exit-node`, and writes
	// the union — preserving the existing per-user dev-tag.
	// AddTag also propagates ListAllNodes errors now (the
	// pre-fix silently swallowed the read error and would
	// have written only `[want]`, silently wiping the
	// existing tags). The `UntagNode` call below
	// (PostAdminExitNodeUntagAsExitNode) already does the
	// same read-modify-write dance for removal, so the
	// read-modify pattern is consistent across both
	// directions. Pinned by 4 unit tests in
	// internal/headscale/tags_test.go (PreservesExistingTags,
	// NoOpWhenAlreadyPresent, PreservesOnError,
	// TagNode_ReplacesEntireSet).
	if err := hs.AddTag(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("tag: "+err.Error()), http.StatusSeeOther)
		return
	}

	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_tag",
		fmt.Sprintf("node=%s id=%d approved_routes=%d tag=tag:exit-node",
			target.Hostname, nodeID, approved))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
		fmt.Sprintf("%s is now tagged as exit-node (%d routes approved)",
			target.Hostname, approved)), http.StatusSeeOther)
}

// PostAdminExitNodeUntagAsExitNode is the v0.18.1
// "Untag" button on /admin/exit-nodes. Removes
// `tag:exit-node` from a node. Useful when the
// operator wants to demote a relay back to a
// regular node (e.g. the relay is going down for
// maintenance and they don't want tailnet clients
// to pick it as an exit-node).
//
// The handler does NOT touch the approved routes —
// those stay as-is. To remove the routes too, the
// operator has to run `docker exec headscale headscale
// nodes approve-routes -i <id> -r "" --force` (or
// similar); we don't expose that from the UI because
// route removal is rarely wanted.
func (s *Service) PostAdminExitNodeUntagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := r.FormValue("node_id")
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	hs := s.HSGlobalFn()
	// UntagNode preserves the other tags (replaces the
	// full tag list, leaving the others in place). If
	// the node was tagged only with tag:exit-node, it
	// falls back to tag:private so headscale keeps at
	// least one tag (the headscale CLI rejects empty
	// tag sets).
	if err := hs.UntagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("untag: "+err.Error()), http.StatusSeeOther)
		return
	}
	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_untag",
		fmt.Sprintf("node_id=%d tag=tag:exit-node", nodeID))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Removed tag:exit-node from node."), http.StatusSeeOther)
}

// ensureExitServers walks every headscale node and INSERT
// OR IGNOREs a row in exit_servers for any node that either
// (a) has an exit-node tag, or (b) advertises any routes.
// The "OR IGNORE" preserves the operator's manual row
// (possibly with enabled=0) so the discovery pass can't
// accidentally re-enable a node the operator disabled.
//
// 2026-07-31: v0.32.7 — exclude subnet-routers. Pre-fix
// `ensureExitServers` also matched any node that advertises
// any routes (condition b), which incorrectly included
// per-user subnet-routers (e.g. skygate-subnet-admin with
// tag:subnet-router advertising 10.0.1.0/24). The subnet-router
// is a LAN bridge for the tailnet, not an exit-node — it
// doesn't route traffic to the internet, doesn't have the
// tag:exit-* role, and shouldn't appear on /admin/exit-nodes.
// The fix: also skip nodes whose tags contain
// `tag:subnet-router` (and the `tag:dev-*` family which is
// the per-device v0.28.0 marker for user devices — those
// don't belong on an exit-node admin page either). A
// `tag:public`-only node with subnet routes is still
// included (public-tagged nodes are the relays that may
// legitimately advertise both 0.0.0.0/0 and a /32 set).
// shouldIncludeAsExitServer is the pure filter extracted from
// ensureExitServers (v0.32.7). Returns true if a node with
// the given tags + available-route count should appear on
// /admin/exit-nodes.
//
// Exclusion rules (added 2026-07-31, v0.32.7):
//   - tag:subnet-router → false (it's a LAN bridge, not an exit)
//   - tag:dev-*        → false UNLESS the node ALSO has a
//     tag:exit-node tag (v0.33.1.30 B82 override: a per-user
//     device that the operator has explicitly promoted to
//     exit-node IS an exit node — the v0.32.7 default of
//     excluding every tag:dev-* node was too aggressive for
//     the case where the operator wants a per-user-tagged
//     workstation to also act as an exit-node for the
//     tailnet. Real-world example: emilia/karolina/sharlotta
//     on the live VM — tagged as `tag:dev-skyadmin-<name>`
//     for the per-user ACL grant AND actually used as
//     exit-nodes via device_rules.exit_node_id references)
//
// Inclusion rules:
//   - any tag:exit-* tag → true
//   - has 1+ advertised route → true
//
// 2026-07-31: extracted from ensureExitServers so the filter
// logic is unit-testable without a live headscale.
// 2026-08-09: v0.33.1.30 B82 — `tag:dev-* + tag:exit-node`
// combo now passes the filter (the v0.32.7 default
// excluded all per-user devices; the v0.33.1.29 B81 fix
// surfaced this for operators who tagged their real
// exit-nodes as `tag:dev-skyadmin-*` and lost them to the
// B21 cleanup pass).
func shouldIncludeAsExitServer(tags []string, availableRouteCount int) bool {
	hasExitTag := false
	isSubnetRouter := false
	isPerUserDevice := false
	for _, t := range tags {
		if strings.Contains(t, "exit-node") {
			hasExitTag = true
		}
		if t == "tag:subnet-router" {
			isSubnetRouter = true
		}
		if strings.HasPrefix(t, "tag:dev-") {
			isPerUserDevice = true
		}
	}
	// tag:subnet-router is ALWAYS excluded — a LAN bridge is
	// not an exit-node regardless of other tags (the v0.32.7
	// intent: don't pollute the exit-nodes page with subnet
	// routers the operator didn't ask for).
	if isSubnetRouter {
		return false
	}
	// v0.33.1.30 B82 override: a per-user device that ALSO
	// has an explicit tag:exit-node IS an exit node (the
	// operator's intent — they tagged it themselves with
	// the standard exit-node tag). The original v0.32.7
	// default was "tag:dev-* → always excluded" which
	// silently removed emilia/karolina/sharlotta from
	// /admin/exit-nodes even though they were actively
	// used as exit-nodes via device_rules.exit_node_id.
	if isPerUserDevice && !hasExitTag {
		return false
	}
	return hasExitTag || availableRouteCount > 0
}

func (s *Service) ensureExitServers() {
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		return
	}
	// Index nodes by ID for the cleanup pass below.
	nodeByID := make(map[string]headscale.NodeView, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	// Step 1: insert any node that should be in
	// exit_servers. The "OR IGNORE" preserves the
	// operator's manual row (possibly with enabled=0) so
	// the discovery pass can't accidentally re-enable a
	// node the operator disabled.
	for _, n := range nodes {
		if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
			db.InsertIgnoreExitServerOnDiscovery(s.dbc(), n.ID, n.GivenName, strings.Join(n.IPAddresses, ","))
		}
	}
	// Step 2 (v0.32.7): clean up rows that the pre-fix
	// filter would have included but the new one excludes
	// (e.g. skygate-subnet-admin with tag:subnet-router
	// that was inserted into exit_servers before the
	// v0.32.7 fix tightened the filter). Without this,
	// the stale row would keep showing up on
	// /admin/exit-nodes even after the new code excludes
	// it from inserts.
	//
	// We only delete rows whose headscale node still exists
	// (node_id is in our current node list) and now fails
	// the filter. Rows for nodes that disappeared from
	// headscale are operator artifacts (e.g. the user
	// deleted the node from the tailnet) — leave those
	// alone; the operator can `kill` them via the
	// /admin/exit-nodes page or directly in the DB.
	rows, _ := s.dbc().Query("SELECT id, node_id FROM exit_servers")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var nid string
			if err := rows.Scan(&id, &nid); err != nil {
				continue
			}
			n, ok := nodeByID[nid]
			if !ok {
				continue // node gone from headscale — leave row
			}
			if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
				continue // still qualifies — keep row
			}
			// Node no longer qualifies — delete the row.
			// Best-effort: errors are logged but not fatal
			// (the next page load will retry).
			// 2026-08-05 v0.33.1.12: db.PlaceholdersList(1) so
			// "?" → "$1" on PG. Without this the auto-cleanup
			// (which fires from the background discovery loop)
			// silently fails on PG and the next page load
			// retries forever.
			if _, err := s.dbc().Exec("DELETE FROM exit_servers WHERE id = "+db.PlaceholdersList(1), id); err != nil {
				s.Backend.Audit(0, "skygate", "exit_server_cleanup_failed",
					fmt.Sprintf("node_id=%s id=%d: %v", nid, id, err))
			}
		}
	}
}

// firstNonEmptyStr returns a unless it is empty, in which case it returns
// b. Added 2026-09-18 (R6) so a handler can prefer an operator-supplied
// ?err= flash over its own internally-generated one without an if-block
// at every call site.
func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
