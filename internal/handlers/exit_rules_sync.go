package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"encoding/json"
)


// exit_rules_sync.go — extracted from exit_rules.go.
// Contains: SyncAdvertisedRoutes, staggeredSync, SyncAdvertisedRoutesHandler,
// DomainAutoUpdater, resolveDomainSubdomains, logAutoUpdate,
// RunDomainAutoUpdater, lookupAcceptRoutes.
// Pure data-plane (advertised-routes sync + DNS autoupdater). The HTTP handler
// (SyncAdvertisedRoutesHandler) is here too because it just calls
// SyncAdvertisedRoutes() and returns JSON.

// SyncAdvertisedRoutes collects all enabled IP/subnet rules and pushes to exit nodes.
func (a *App) SyncAdvertisedRoutes() map[string]string {
	result := map[string]string{}
	rows, err := a.DB.Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id")
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
		msg, err := a.HS.SetAdvertisedRoutes(node, approveRoutes, a.lookupAcceptRoutes(node))
		if err != nil {
			result[node] = "ssh: " + err.Error()
		} else {
			result[node] = "ok"
			_ = msg
		}
		// Approve all routes (including base 0.0.0.0/0, ::/0) for this exit
		// node via headscale CLI (docker exec).
		// 2026-07-08: pass full list (base + per-rule) so the node keeps
		// its exit-node capability (default route advertised AND approved).
		if approved, approveErr := a.HS.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
			result[node+"_approve_err"] = approveErr.Error()
			result[node] = "ssh:ok approve:err=" + approveErr.Error()
		} else if approved > 0 {
			result[node] = fmt.Sprintf("ok approved=%d", approved)
		}
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
// batch survived. For karolina (145 rules) that meant roughly 7 of 8 subnets
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
func (a *App) staggeredSync() {
	if a.Cfg == nil || !a.Cfg.StaggerSync {
		a.SyncAdvertisedRoutes()
		return
	}
	interval := a.Cfg.StaggerInterval
	if interval <= 0 { interval = 30 * time.Second }
	// Collect exit_nodes with their rule counts
	rows, _ := a.DB.Query("SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id != '' GROUP BY exit_node_id")
	if rows == nil {
		a.SyncAdvertisedRoutes()
		return
	}
	defer rows.Close()
	type nodeRules struct { name string; count int }
	var nodes []nodeRules
	totalRules := 0
	for rows.Next() {
		var n string; var c int
		if rows.Scan(&n, &c) == nil {
			nodes = append(nodes, nodeRules{n, c})
			totalRules += c
		}
	}
	if len(nodes) == 0 {
		a.SyncAdvertisedRoutes()
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
			rules, _ := a.DB.Query("SELECT target_value FROM device_rules WHERE enabled=1 AND exit_node_id=? AND target_type IN ('subnet', 'ip')", n.name)
			if rules == nil { continue }
			var routeList []string
			for rules.Next() {
				var v string
				if rules.Scan(&v) == nil { routeList = append(routeList, v) }
			}
			rules.Close()
			// Always include base exit-node routes.
			batch := []string{"0.0.0.0/0", "::/0"}
			seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
			for _, r := range routeList {
				if !seen[r] { seen[r] = true; batch = append(batch, r) }
			}
			log.Printf("staggeredSync(aggregated): %s advertising %d unique routes (was: per-batch, lost all but last batch)",
				n.name, len(batch))
			msg, _ := a.HS.SetAdvertisedRoutes(n.name, batch, a.lookupAcceptRoutes(n.name))
			// 2026-07-11: `tailscale set` on unix exits 0 with empty stdout, so
			// `msg` is often "". Render an "ok" marker instead of a dangling colon.
			if strings.TrimSpace(msg) == "" {
				msg = "ok"
			}
			log.Printf("staggeredSync(aggregated): %s advertised: %s", n.name, msg)
			if _, err := a.HS.ApproveAllRoutesWithList(n.name, batch); err != nil {
				log.Printf("staggeredSync(aggregated): %s approve err: %v", n.name, err)
			}
			time.Sleep(interval)
		}
		log.Printf("staggeredSync(aggregated): done")
	}()
}

// SyncAdvertisedRoutesHandler triggers route sync (admin only).
func (a *App) SyncAdvertisedRoutesHandler(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, 403)
		return
	}
	result := a.SyncAdvertisedRoutes()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// knownSubdomains maps a main domain to its known subdomain hosts for static assets.
// 2026-07-07: issue #9 — Cloudflare-routed sites have static on different subdomains.
var knownSubdomains = map[string][]string{
	"rutracker.org": {"static.rutracker.cc"},
	"rutracker.cc":  {"static.rutracker.cc"},
}

// 2026-07-07: issue #6 — DomainAutoUpdater
// Background job: resolves all domain rules every interval, reconciles with /32 IP rules.
// Returns count of changes (added + removed) and writes log entries.
func (a *App) DomainAutoUpdater() (added, removed int, err error) {
	rows, qerr := a.DB.Query("SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'")
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
		addrs, lerr := net.LookupHost(d.domain)
		if lerr != nil {
			a.logAutoUpdate(d.id, d.domain, 0, 0, "lookup failed: "+lerr.Error())
			continue
		}
		currentIPs := map[string]bool{}
		for _, a := range addrs {
			if strings.Contains(a, ":") { continue } // skip IPv6
			currentIPs[a] = true
		}
		if extraIPs := a.resolveDomainSubdomains(d.domain); extraIPs != nil {
			for ip := range extraIPs { currentIPs[ip] = true }
		}

		// Get existing /32 rules for this domain
		existing := map[string]int{} // IP -> rule id
		rows2, eerr := a.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND target_value LIKE '%/32'",
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
		rows3, _ := a.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain,'')=?",
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
			if _, exists := all32[ip]; exists { continue }
			// 2026-07-09: проверяем, нет ли уже /32 с этим target_value
			// (под другим parent_domain — shared IP между доменами).
			// Не дублируем — autoupdater другого домена уже покрыл.
			var existingSharedID int
			_ = a.DB.QueryRow(
				"SELECT id FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND target_value=? LIMIT 1",
				d.userID, d.deviceID, d.exitNode, ip+"/32").Scan(&existingSharedID)
			if existingSharedID > 0 { continue }
			if _, ierr := a.DB.Exec(
				"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, 'subnet', ?, ?, ?, ?)",
				d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, d.domain); ierr == nil {
				added++
			}
		}
		// Remove old IPs
		for ip, rid := range all32 {
			if currentIPs[ip] { continue }
			if _, derr := a.DB.Exec("DELETE FROM device_rules WHERE id=?", rid); derr == nil {
				removed++
			}
		}

		if len(currentIPs) > 0 || len(all32) > 0 {
			a.logAutoUpdate(d.id, d.domain, added, removed, "")
		}
	}

	return added, removed, nil
}


// resolveDomainSubdomains resolves known subdomains and (optionally) fetches
// the main page to discover subdomains from href/src attributes. Returns a set
// of IPv4 addresses to add to the rule list.
func (a *App) resolveDomainSubdomains(domain string) map[string]bool {
	httpClient := &http.Client{Timeout: 8 * time.Second}
	var body []byte

	// Check known subdomains first (fast path)
	ips := map[string]bool{}
	for _, sd := range knownSubdomains[domain] {
		if addrs, err := net.LookupHost(sd); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") { ips[ip] = true }
			}
		}
	}
	if len(ips) > 0 {
		a.logAutoUpdate(0, domain, len(ips), 0, "known subdomains resolved: "+strconv.Itoa(len(knownSubdomains[domain])))
		return ips
	}

	for _, scheme := range []string{"https", "http"} {
		resp, err := httpClient.Get(scheme + "://" + domain + "/")
		if err != nil { continue }
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 { return nil }

	subdomains := map[string]bool{}
	hostRe := regexp.MustCompile(`(?:href|src)=["\']https?://([^/\s"\']+)`)
	for _, m := range hostRe.FindAllStringSubmatch(string(body), -1) {
		host := m[1]
		// Skip self and subdomains of self
		if host == domain || strings.HasSuffix(host, "."+domain) { continue }
		subdomains[host] = true
	}
	for host := range subdomains {
		if addrs, err := net.LookupHost(host); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") { ips[ip] = true }
			}
		}
	}
	if len(ips) > 0 {
		a.logAutoUpdate(0, domain, len(ips), 0, "subdomains resolved: "+strconv.Itoa(len(subdomains)))
	}
	return ips
}

func (a *App) logAutoUpdate(ruleID int, domain string, added, removed int, errMsg string) {
	detail := fmt.Sprintf("domain=%s added=%d removed=%d", domain, added, removed)
	if errMsg != "" {
		detail += " err=" + errMsg
	}
	_, _ = a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (0, 'autoupdate', ?)", detail)
}

func (a *App) RunDomainAutoUpdater(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("autoupdater: disabled (interval=0)")
		return
	}
	log.Printf("autoupdater: starting (interval=%s)", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	// Run once immediately, then on tick
	added, removed, err := a.DomainAutoUpdater()
	if err != nil {
		log.Printf("autoupdater: initial: %v", err)
	} else if added > 0 || removed > 0 {
		log.Printf("autoupdater: initial: added=%d removed=%d", added, removed)
		a.staggeredSync() // 2026-07-07: issue #12 — staggered
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("autoupdater: stopping")
			return
		case <-t.C:
			added, removed, err := a.DomainAutoUpdater()
			if err != nil {
				log.Printf("autoupdater: %v", err)
				continue
			}
			if added > 0 || removed > 0 {
				log.Printf("autoupdater: added=%d removed=%d, syncing exit-nodes", added, removed)
				a.staggeredSync() // 2026-07-07: issue #12
			}
		}
	}
}

// lookupAcceptRoutes returns the per-exit-node Tailscale AcceptRoutes
// preference stored in exit_servers.accept_routes:
//   -1 -> --accept-routes=false (nodes that co-host another VPN, e.g. Amnezia-AWG)
//    0 -> unset, do not change AcceptRoutes on the node
//    1 -> --accept-routes=true
//
// Lookup is keyed on the node's hostname. Falls back to 0 (do not change)
// if the node is not in exit_servers or the column is missing.
func (a *App) lookupAcceptRoutes(nodeHostname string) int {
	if a == nil || a.DB == nil || nodeHostname == "" {
		return 0
	}
	var accept int
	err := a.DB.QueryRow("SELECT accept_routes FROM exit_servers WHERE hostname = ? LIMIT 1", nodeHostname).Scan(&accept)
	if err != nil {
		return 0
	}
	return accept
}
