// exit_nodes_page.go — GET /admin/exit-nodes.
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01). AdminExitNodes
// is ~380 lines on its own: it assembles ExitNodeInfo rows from headscale, the
// monitoring snapshots, the relay list and the prefix-assignment table, then
// renders the page. Keeping it apart from the POST handlers means a UI change
// no longer scrolls past 1000 lines of write paths.

package admin

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

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
	// B312: one read for every relay's location (the same map the assignment engine
	// uses), so the table can show WHERE each relay is and the operator can tell
	// "same country as the relay I just lost" at a glance.
	exitLocations := db.ListExitLocations(s.dbc())
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
		// B312: the relay's place on the map (manual wins over the automatic lookup;
		// an empty row stays "unknown" rather than pretending to be somewhere).
		if loc, ok := exitLocations[strings.ToLower(strings.TrimSpace(e.Hostname))]; ok && loc.Known() {
			n.Location = strings.TrimSpace(loc.Label)
			if n.Location == "" {
				n.Location = strings.TrimSpace(loc.Country)
			}
			n.LocationSource = strings.TrimSpace(loc.Source)
			n.LocationKnown = true
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
