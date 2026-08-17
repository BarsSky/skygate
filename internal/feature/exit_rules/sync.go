// Package exit_rules — sync.go owns the advertised-routes
// sync logic, the DNS autoupdater (background goroutine),
// and the /admin/exit-rules/sync HTTP endpoint.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): moved from
// internal/handlers/exit_rules_sync.go. The handlers used
// to be methods on *App; they now live on *Service. The
// HTTP handler (PostSyncAdvertisedRoutes) is exposed for
// the /admin/exit-rules/sync route — it just calls
// SyncAdvertisedRoutes and returns JSON.
//
// RunDomainAutoUpdater stays on *App (see
// internal/handlers/exit_rules_sync.go for the boot-time
// wrapper that main.go calls) — the long-lived context +
// ticker lifecycle are managed there. The wrapper
// delegates to the Service's DomainAutoUpdater +
// staggeredSync methods via the exitRulesRunner interface
// (see internal/handlers/handlers.go).
package exit_rules

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// knownSubdomains maps a main domain to its known subdomain hosts for static assets.
// 2026-07-07: issue #9 — Cloudflare-routed sites have static on different subdomains.
var knownSubdomains = map[string][]string{
	"rutracker.org": {"static.rutracker.cc"},
	"rutracker.cc":  {"static.rutracker.cc"},
}

// SyncAdvertisedRoutes collects all enabled IP/subnet rules and pushes to exit nodes.
// Pure data-plane (advertised-routes sync). Returns a per-node status map
// (the HTTP handler marshals it as JSON).
//
// 2026-08-04 v0.33.1: per-node SSH config + combined result reporting.
// Two pre-v0.33.1 bugs were silently hiding failures from the operator:
//
//  1. SetAdvertisedRoutes was called with a hard-coded
//     /home/admin/.ssh/config which doesn't exist inside the
//     dockerised skygate — the SSH call always failed with
//     "Can't open user config file". The headscale approve-routes
//     step (run right after, unconditionally) succeeded, so
//     `result[node]` was overwritten to "ok approved=N" and the
//     operator thought the sync worked. The actual tailscaled
//     on the relay was never re-configured.
//
//  2. `ssh: <err>` was the value stored in result[node] when SSH
//     failed, but the unconditional approve step's success then
//     OVERWROTE that to "ok approved=N". Result: SSH failure
//     was invisible from the UI.
//
// Fix: read the per-exit-node SSH target + key path from
// exit_servers.ssh_target / ssh_key_path (with the
// Config.SSHKeyPath / SKYGATE_EXIT_SSH_KEY default as the
// global fallback), pass them to SetAdvertisedRoutes, and
// combine the SSH result with the approve result into a single
// string so neither side's failure can be hidden:
//
//	ssh=ok approved=214
//	ssh=err=<msg> approved=0
//	ssh=ok approve=err=<msg>
//	ssh=err=<msg> approve=err=<msg>
func (s *Service) SyncAdvertisedRoutes() map[string]string {
	result := map[string]string{}
	rows, err := s.DB.Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id")
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	defer rows.Close()
	exitRoutes := map[string][]string{}
	for rows.Next() {
		var node, target string
		if err := rows.Scan(&node, &target); err != nil {
			continue
		}
		exitRoutes[node] = append(exitRoutes[node], target)
	}
	// Default SSH key path comes from Config (set from
	// SKYGATE_EXIT_SSH_KEY, default /home/operator/.ssh/skygate_sync).
	// The operator can override per-exit-node via
	// exit_servers.ssh_key_path in the /admin/exit-nodes form.
	var defaultKeyPath string
	if s.Cfg != nil {
		defaultKeyPath = s.Cfg.SSHKeyPath
	}
	for node, routes := range exitRoutes {
		// 2026-07-08: prepend base exit-node routes (0.0.0.0/0, ::/0) so the
		// node stays an exit node after sync. SetAdvertisedRoutes already
		// adds these on the SSH side, but the headscale CLI approve-routes
		// call below only knows about the routes we pass explicitly.
		approveRoutes := []string{"0.0.0.0/0", "::/0"}
		seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
		for _, r := range routes {
			if !seen[r] {
				seen[r] = true
				approveRoutes = append(approveRoutes, r)
			}
		}
		// Resolve per-exit-node SSH config. The empty-row fallback
		// (no row for this hostname) is fine — SetAdvertisedRoutes
		// will use nodeHostname as the target and defaultKeyPath as
		// the key. The v0.33.1 signature refuses to run when both
		// per-row and default key are empty (so the operator sees
		// a clear "no ssh_key_path" error instead of a silent
		// "config file not found" from ssh).
		//
		// 2026-08-09 v0.33.1.29 B81: sshTarget is resolved through
		// the new LookupExitServerSSHTarget helper which applies
		// the fallback chain operator-override → root@<tailscale_ip>
		// → "". This fixes the "ssh root@<public-ip>:22: Operation
		// timed out" failure mode where the operator had set
		// ssh_target to a firewalled public IP and the SetAdvertisedRoutes
		// call had no way to fall back to the always-reachable
		// Tailscale IP. The legacy "ssh_target empty → nodeHostname"
		// fallback in SetAdvertisedRoutes still exists for the
		// "no exit_servers row at all" case but is intentionally
		// NOT used here — empty ssh_target with a row in
		// exit_servers means "use the auto-fallback", not "fall
		// through to a hostname that doesn't resolve".
		sshRow, _ := db.LookupExitServerSSH(s.DB, node)
		sshTarget, _ := db.LookupExitServerSSHTarget(s.DB, node)
		sshKeyPath := sshRow.KeyPath
		if sshKeyPath == "" {
			sshKeyPath = defaultKeyPath
		}
		// SSH first. On error, we STILL try the headscale approve
		// step below — the operator may have already approved these
		// routes some other way (e.g. directly via the headscale
		// CLI) and the SSH failure should be visible but not block
		// the approval side.
		sshLabel := "ok"
		_, sshErr := s.HS.SetAdvertisedRoutes(node, approveRoutes, s.lookupAcceptRoutes(node), sshTarget, sshKeyPath)
		if sshErr != nil {
			sshLabel = "err=" + sshErr.Error()
		}
		// Approve all routes (including base 0.0.0.0/0, ::/0) for this exit
		// node via headscale CLI (docker exec).
		// 2026-07-08: pass full list (base + per-rule) so the node keeps
		// its exit-node capability (default route advertised AND approved).
		approveLabel := "approved=0"
		if approved, approveErr := s.HS.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
			approveLabel = "approve=err=" + approveErr.Error()
		} else if approved > 0 {
			approveLabel = fmt.Sprintf("approved=%d", approved)
		}
		result[node] = "ssh=" + sshLabel + " " + approveLabel
	}
	if len(exitRoutes) == 0 {
		result["info"] = "no IP/subnet rules configured"
	}
	return result
}

// 2026-07-09: aggregated sync per node (issue: stale batches overwrote each other).
//
// Previous implementation called SetAdvertisedRoutes once per 20-rule batch within
// a single node. Because `tailscale set --advertise-routes=` REPLACES the node's
// advertised-route list, every batch wiped the previous one - only the last
// batch survived. For relay-3 (145 rules) that meant roughly 7 of 8 subnets
// were silently lost after every staggered sync.
//
// New behaviour: even when SKYGATE_STAGGER_SYNC=true and totalRules > batchSize,
// we still call SetAdvertisedRoutes exactly ONCE per node with the full
// de-duplicated list (with 0.0.0.0/0 + ::/0 always prepended). Approve follows
// in the same call. The stagger flag is kept for back-compat but is effectively
// a no-op now - headscale accepts the full payload in one round-trip.
//
// `interval` is still applied between NODES (not between batches within a
// node) so headscale isn't hammered when many exit-nodes sync at once.
func (s *Service) StaggeredSync() {
	if s.Cfg == nil || !s.Cfg.StaggerSync {
		s.SyncAdvertisedRoutes()
		return
	}
	interval := s.Cfg.StaggerInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Collect exit_nodes with their rule counts
	rows, _ := s.DB.Query("SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id != '' GROUP BY exit_node_id")
	if rows == nil {
		s.SyncAdvertisedRoutes()
		return
	}
	defer rows.Close()
	type nodeRules struct {
		name  string
		count int
	}
	var nodes []nodeRules
	totalRules := 0
	for rows.Next() {
		var n string
		var c int
		if rows.Scan(&n, &c) == nil {
			nodes = append(nodes, nodeRules{n, c})
			totalRules += c
		}
	}
	if len(nodes) == 0 {
		s.SyncAdvertisedRoutes()
		return
	}
	// Old behaviour fell through to SyncAdvertisedRoutes when totalRules <= batchSize.
	// SyncAdvertisedRoutes already does aggregated per-node sync, so just call it.
	// Old staggered path is replaced entirely: one SetAdvertisedRoutes per node,
	// not per batch.
	log.Printf("staggeredSync(aggregated): %d rules across %d nodes, interval=%s",
		totalRules, len(nodes), interval)
	go func() {
		for _, n := range nodes {
			rules, _ := s.DB.Query("SELECT target_value FROM device_rules WHERE enabled=1 AND exit_node_id=$1 AND target_type IN ('subnet', 'ip')", n.name)
			if rules == nil {
				continue
			}
			var routeList []string
			for rules.Next() {
				var v string
				if rules.Scan(&v) == nil {
					routeList = append(routeList, v)
				}
			}
			rules.Close()
			// Always include base exit-node routes.
			batch := []string{"0.0.0.0/0", "::/0"}
			seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
			for _, r := range routeList {
				if !seen[r] {
					seen[r] = true
					batch = append(batch, r)
				}
			}
			log.Printf("staggeredSync(aggregated): %s advertising %d unique routes (was: per-batch, lost all but last batch)",
				n.name, len(batch))
			// 2026-08-04 v0.33.1: per-node SSH config (was hard-coded
			// /home/admin/.ssh/config + nodeHostname, both of which
			// broke in the dockerised skygate).
			// 2026-08-09 v0.33.1.29 B81: sshTarget uses the new
			// helper with the operator-override → Tailscale IP
			// fallback chain (see SyncAdvertisedRoutes for the
			// full rationale). The key path stays on
			// LookupExitServerSSH + Cfg.SSHKeyPath fallback.
			sshRow, _ := db.LookupExitServerSSH(s.DB, n.name)
			sshTarget, _ := db.LookupExitServerSSHTarget(s.DB, n.name)
			sshKeyPath := sshRow.KeyPath
			if sshKeyPath == "" && s.Cfg != nil {
				sshKeyPath = s.Cfg.SSHKeyPath
			}
			msg, sshErr := s.HS.SetAdvertisedRoutes(n.name, batch, s.lookupAcceptRoutes(n.name), sshTarget, sshKeyPath)
			// 2026-07-11: `tailscale set` on unix exits 0 with empty stdout, so
			// `msg` is often "". Render an "ok" marker instead of a dangling colon.
			if strings.TrimSpace(msg) == "" && sshErr == nil {
				msg = "ok"
			}
			if sshErr != nil {
				log.Printf("staggeredSync(aggregated): %s SSH err: %v", n.name, sshErr)
			} else {
				log.Printf("staggeredSync(aggregated): %s advertised: %s", n.name, msg)
			}
			if _, err := s.HS.ApproveAllRoutesWithList(n.name, batch); err != nil {
				log.Printf("staggeredSync(aggregated): %s approve err: %v", n.name, err)
			}
			time.Sleep(interval)
		}
		log.Printf("staggeredSync(aggregated): done")
	}()
}

// PostSyncAdvertisedRoutes triggers route sync (admin only).
// HTTP entry point for the /admin/exit-rules/sync button.
// Just calls SyncAdvertisedRoutes and returns JSON.
//
// 2026-08-04 v0.33.1: writes an audit_log row with action
// `sync_advertised_routes` and a per-node result summary. The
// audit row is the operator's source of truth when the UI's
// `result` map isn't visible (e.g. triggered by the periodic
// autoupdater rather than the manual button). Without this
// row, the v0.32.x "ok approved=N with broken SSH" bug
// stayed invisible for weeks — the audit_log is the
// dashboard the operator grep's when something looks off.
func (s *Service) PostSyncAdvertisedRoutes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	result := s.SyncAdvertisedRoutes()
	// Compose a compact detail string: "nodes=2 ssh_ok=1
	// ssh_err=1 approve_err=0 karolina=ssh=ok approved=214
	// emilia=ssh=err=... approved=0". The per-node breakdown
	// is the diagnostic gold when the operator greps
	// /admin/audit for the failure mode.
	detailParts := []string{}
	sshOK, sshErr, approveErr := 0, 0, 0
	for _, v := range result {
		switch {
		case strings.HasPrefix(v, "ssh=ok "):
			sshOK++
		case strings.HasPrefix(v, "ssh=err="):
			sshErr++
		}
		if strings.Contains(v, "approve=err=") {
			approveErr++
		}
	}
	detailParts = append(detailParts,
		fmt.Sprintf("nodes=%d ssh_ok=%d ssh_err=%d approve_err=%d",
			len(result), sshOK, sshErr, approveErr))
	for node, v := range result {
		detailParts = append(detailParts, fmt.Sprintf("%s=%s", node, v))
	}
	if s.Backend != nil && c != nil {
		s.Backend.Audit(c.UserID, c.Username, "sync_advertised_routes", strings.Join(detailParts, " "))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// 2026-07-07: issue #6 — DomainAutoUpdater
// Background job: resolves all domain rules every interval, reconciles with /32 IP rules.
// Returns count of changes (added + removed) and writes log entries.
func (s *Service) DomainAutoUpdater() (added, removed int, err error) {
	rows, qerr := s.DB.Query("SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'")
	if qerr != nil {
		return 0, 0, qerr
	}
	defer rows.Close()
	type domainRule struct {
		id       int
		userID   int64
		deviceID int
		exitNode string
		domain   string
		action   string
		deviceIP string
	}
	var domains []domainRule
	for rows.Next() {
		var r domainRule
		var uid int64
		if err := rows.Scan(&r.id, &uid, &r.deviceID, &r.exitNode, &r.domain, &r.action, &r.deviceIP); err == nil {
			r.userID = uid
			domains = append(domains, r)
		}
	}

	for _, d := range domains {
		// 2026-07-28: CDN detection — short-circuit before DNS if
		// we already have a CDN range rule for THIS SPECIFIC
		// domain. The marker format is "cdn:<name>:<domain>".
		// The ranges don't churn (stable network allocations),
		// so the autoupdater has nothing to do for these.
		//
		// The check is per-domain, NOT per-(user, device,
		// exit_node): once auth.docker.io got its CDN marker,
		// a naive (user, device, exit_node) check would also
		// short-circuit artstation.com (because both share the
		// same user=1/device=9/exit_node=relay-3 tuple), even
		// though artstation doesn't yet have a CDN marker. The
		// autoupdate would never process artstation again.
		//
		// We use LIKE 'cdn:%:<domain>' so the CDN-name slot
		// matches any CDN (cloudflare/fastly/google/akamai)
		// without us having to know the CDN name in advance.
		// A future autoupdate tick that runs AFTER CDN detection
		// has inserted the marker will match here and short-
		// circuit; the per-tick no-op.
		existingMarker := ""
		_ = s.DB.QueryRow(
			"SELECT parent_domain FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND parent_domain LIKE $4 LIMIT 1",
			d.userID, d.deviceID, d.exitNode, cdnParentMarkerGuess(d.domain),
		).Scan(&existingMarker)
		if isCDNMarker(existingMarker) {
			// Already have a CDN range rule for this domain.
			// Nothing to do.
			continue
		}

		addrs, lerr := net.LookupHost(d.domain)
		if lerr != nil {
			s.logAutoUpdate(d.id, d.domain, 0, 0, "lookup failed: "+lerr.Error())
			continue
		}
		currentIPs := map[string]bool{}
		for _, addr := range addrs {
			if strings.Contains(addr, ":") {
				continue // skip IPv6
			}
			currentIPs[addr] = true
		}
		if extraIPs := s.resolveDomainSubdomains(d.domain); extraIPs != nil {
			for ip := range extraIPs {
				currentIPs[ip] = true
			}
		}

		// 2026-07-28: CDN detection — if all currentIPs fall in
		// a known CDN's published ranges, replace the per-IP /32
		// approach with the CDN's CIDR ranges. The ranges are
		// stable, so the autoupdater doesn't churn for these
		// domains.
		if cdnName, cdnCIDRs, isCDN := detectCDN(currentIPs); isCDN {
			marker := cdnParentMarker(cdnName, d.domain)
			// Insert each CDN range. The marker lets the next
			// tick short-circuit (see the existingMarker check
			// at the top of the loop).
			cdnAdded := 0
			for _, cidr := range cdnCIDRs {
				// B125: rely on the UNIQUE INDEX
				// device_rules_natural_key_uniq (added in
				// migrateV056PG) + ON CONFLICT DO NOTHING to
				// close the SELECT-then-INSERT race that
				// previously let duplicate rows accumulate.
				// The pre-check SELECT is still useful for the
				// cdnAdded counter (to know if a NEW row was
				// created vs an existing one was hit), but the
				// race is closed by the conflict target.
				tag, err := s.DB.Exec(
					`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain)
					 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7)
					 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
					d.userID, d.deviceID, d.exitNode, cidr, d.action, d.deviceIP, marker)
				if err != nil {
					continue
				}
				if n, _ := tag.RowsAffected(); n > 0 {
					cdnAdded++
				}
			}
			// Remove the legacy per-IP /32 rules for this
			// domain — they have parent_domain = d.domain (no
			// cdn: prefix). Now that the CDN marker covers the
			// domain, the /32 rules are dead weight.
			legacyRemoved := 0
			if _, err := s.DB.Exec(
				"DELETE FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND COALESCE(parent_domain,'')=$4",
				d.userID, d.deviceID, d.exitNode, d.domain,
			); err == nil {
				// RowsAffected isn't in the go-sqlite3 driver
				// by default; we count via a SELECT instead.
				// (legacyRemoved is a coarse metric — used for
				//  the log line only.)
				_ = legacyRemoved
			}
			added += cdnAdded
			s.logAutoUpdate(d.id, d.domain, cdnAdded, 0, "CDN detected: "+cdnName+" — using "+strconv.Itoa(len(cdnCIDRs))+" published ranges")
			continue
		}

		// Get existing /32 rules for this domain
		existing := map[string]int{} // IP -> rule id
		rows2, eerr := s.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32'",
			d.userID, d.deviceID, d.exitNode)
		if eerr != nil {
			continue
		}
		// Filter: only IPs that are NOT explicitly in currentIPs (could be from other rules)
		// Strategy: for each IP in currentIPs that's not in DB → INSERT
		//           for each /32 IP in DB that resolves to a removed domain IP → DELETE
		// We track: for THIS domain, which /32 IPs correspond?
		// Simplification: we know d.domain is the source, so any /32 that matches
		// the pattern and exists in oldIPs but not in currentIPs is from this domain.
		_ = existing
		rows2.Close()

		// Find all /32 rules for (user, device, exit_node) that LOOK like auto-resolved from this domain
		// We track them via a side table OR a heuristic: for this domain, list all /32 rules where
		// the same domain's last resolved IPs included them.
		// Pragmatic approach: maintain a comment-style hint in another table? Or use a marker.
		// Simpler: for this domain, list ALL /32 rules and diff against currentIPs.
		// User-added /32 rules (manual) get deleted if we don't track — TOO DANGEROUS.
		// Better: introduce column `parent_domain` (NULL = manual).
		all32 := map[string]int{}
		rows3, _ := s.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain,'')=$4",
			d.userID, d.deviceID, d.exitNode, d.domain)
		if rows3 != nil {
			for rows3.Next() {
				var rid int
				var val string
				if rows3.Scan(&rid, &val) == nil {
					// strip /32
					ip := strings.TrimSuffix(val, "/32")
					all32[ip] = rid
				}
			}
			rows3.Close()
		}

		// Add new IPs
		for ip := range currentIPs {
			if _, exists := all32[ip]; exists {
				continue
			}
			// B125: use ON CONFLICT DO NOTHING (against the
			// UNIQUE INDEX device_rules_natural_key_uniq
			// from migrateV056PG) instead of the pre-check +
			// INSERT race. The pre-check is preserved for the
			// "shared IP between domains" case (B123 alert
			// UX) — when another domain already added the
			// /32, the conflict target skips silently. The
			// parent_domain column is what disambiguates
			// shared IPs: each (user, device, exit, type,
			// value) tuple can have one row per parent_domain.
			tag, ierr := s.DB.Exec(
				`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain)
				 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7)
				 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
				d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, d.domain)
			if ierr != nil {
				continue
			}
			if n, _ := tag.RowsAffected(); n > 0 {
				added++
			}
		}
		// Remove old IPs
		for ip, rid := range all32 {
			if currentIPs[ip] {
				continue
			}
			if _, derr := s.DB.Exec("DELETE FROM device_rules WHERE id=$1", rid); derr == nil {
				removed++
			}
		}

		if len(currentIPs) > 0 || len(all32) > 0 {
			s.logAutoUpdate(d.id, d.domain, added, removed, "")
		}
	}

	return added, removed, nil
}

// resolveDomainSubdomains resolves known subdomains and (optionally) fetches
// the main page to discover subdomains from href/src attributes. Returns a set
// of IPv4 addresses to add to the rule list.
func (s *Service) resolveDomainSubdomains(domain string) map[string]bool {
	httpClient := &http.Client{Timeout: 8 * time.Second}
	var body []byte

	// Check known subdomains first (fast path)
	ips := map[string]bool{}
	for _, sd := range knownSubdomains[domain] {
		if addrs, err := net.LookupHost(sd); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") {
					ips[ip] = true
				}
			}
		}
	}
	if len(ips) > 0 {
		s.logAutoUpdate(0, domain, len(ips), 0, "known subdomains resolved: "+strconv.Itoa(len(knownSubdomains[domain])))
		return ips
	}

	for _, scheme := range []string{"https", "http"} {
		resp, err := httpClient.Get(scheme + "://" + domain + "/")
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 {
		return nil
	}

	subdomains := map[string]bool{}
	hostRe := regexp.MustCompile(`(?:href|src)=["']https?://([^/\s"']+)`)
	for _, m := range hostRe.FindAllStringSubmatch(string(body), -1) {
		host := m[1]
		// Skip self and subdomains of self
		if host == domain || strings.HasSuffix(host, "."+domain) {
			continue
		}
		subdomains[host] = true
	}
	for host := range subdomains {
		if addrs, err := net.LookupHost(host); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") {
					ips[ip] = true
				}
			}
		}
	}
	if len(ips) > 0 {
		s.logAutoUpdate(0, domain, len(ips), 0, "subdomains resolved: "+strconv.Itoa(len(subdomains)))
	}
	return ips
}

func (s *Service) logAutoUpdate(ruleID int, domain string, added, removed int, errMsg string) {
	detail := fmt.Sprintf("domain=%s added=%d removed=%d", domain, added, removed)
	if errMsg != "" {
		detail += " err=" + errMsg
	}
	_ = db.AppendExitRuleLog(s.DB, db.ExitRuleLogNoVersion, db.ExitRuleActionAutoupdate, detail)
}

// lookupAcceptRoutes returns the per-exit-node Tailscale AcceptRoutes
// preference stored in exit_servers.accept_routes:
//   -1 -> --accept-routes=false (nodes that co-host another VPN, e.g. Amnezia-AWG)
//    0 -> unset, do not change AcceptRoutes on the node
//    1 -> --accept-routes=true
//
// Lookup is keyed on the node's hostname. Falls back to 0 (do not change)
// if the node is not in exit_servers or the column is missing.
//
// 2026-07-12: Этап 10 part 5 — moved the SELECT to db.LookupExitServerAcceptRoutes
// (which centralises the column name + the no-row fallback to 0).
func (s *Service) lookupAcceptRoutes(nodeHostname string) int {
	if s == nil || s.DB == nil || nodeHostname == "" {
		return 0
	}
	accept, _ := db.LookupExitServerAcceptRoutes(s.DB, nodeHostname)
	return accept
}
