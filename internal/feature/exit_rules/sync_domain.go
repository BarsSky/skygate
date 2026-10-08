// Package exit_rules — sync_domain.go owns the domain auto-updater: the
// background job that resolves every domain rule and reconciles the
// derived /32 and CDN-range rows, plus its log helper and the
// known-subdomain table.
//
// Split out of sync.go (2026-10-08, PURE MOVE — the declarations below are
// byte-identical to what sync.go carried before the split). The ACL apply
// pipeline lives in sync_acl.go, route advertisement in
// sync_routes.go; sync.go keeps the package doc.
package exit_rules

import (
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

// 2026-07-07: issue #6 — DomainAutoUpdater
// Background job: resolves all domain rules every interval, reconciles with /32 IP rules.
// Returns count of changes (added + removed) and writes log entries.
func (s *Service) DomainAutoUpdater() (added, removed int, err error) {
	// B274: collapse the redundant rows the CDN expansion produces.
	// One domain rule for a Cloudflare-fronted site yields the CDN's
	// whole published range set, so N domains of the same CDN create N
	// identical rows per CIDR (live: `basic` carried 5 rows for
	// `104.16.0.0/12`, one per discord.*/rutracker.org parent). The rows
	// are not wrong (parent_domain is part of the natural key by B183),
	// but they inflate rule counts, the admin "expected routes" counter
	// and the prefix-ownership weight. Keep the most informative parent
	// (the cdn:-prefixed one), drop the rest. Pure data hygiene — the
	// ACL is unaffected because the generator collapses CIDRs into one
	// host alias anyway.
	//
	// It runs DEFERRED, after the resolve loop below: the loop inserts
	// one row per (domain, CIDR) pair — that is what creates the
	// duplicates — so deduplicating before it would be undone by the
	// very same pass (observed live: 47 groups right after a tick that
	// started with 0).
	defer func() {
		if n, derr := s.CollapseDuplicateDerivedRules(); derr != nil {
			log.Printf("auto-updater: dedup: %v", derr)
		} else if n > 0 {
			log.Printf("auto-updater: dedup removed %d redundant derived rule row(s)", n)
		}
	}()
	rows, qerr := s.dbc().Query("SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'")
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

	// B308 (v1.5.73): a domain whose A records rotate was re-resolved on EVERY
	// tick, so the derived /32 rows were deleted and re-inserted forever and the
	// generated ACL never stopped changing (live: 39 drift/defer lines in two
	// hours, ±20 rules per tick, the red «политика УСТАРЕЛА» banner effectively
	// permanent and a headscale restart on every throttled re-apply on a
	// policy.mode=file host). The resolution is not wrong — an IP that moved must
	// follow — it just does not need to happen every five minutes, so it is gated
	// by a per-domain minimum interval (default six hours, operator-editable).
	resolveInterval := s.DomainResolveInterval()
	nowUnix := time.Now().Unix()
	skipped := 0
	// B351 (2026-10-05): both INSERTs below are raw (they need DO NOTHING +
	// RowsAffected to count what was newly added, which the canonical
	// qInsertDeviceRule cannot express), and until this block they listed neither
	// user_name nor device_hostname. The one-time V0.44 migration filled
	// user_name once and nothing ever filled it again, so 220 of 349 live rows
	// carried an empty owner and the B348 admin index listed one device twice.
	// The pair is resolved from portal_users / node_owner_map, one query per
	// unique (user, device) per pass.
	owners := newRuleOwnerLookup(s.dbc())
	for _, d := range domains {
		if !s.domainResolveDueFor(d.domain, nowUnix, resolveInterval) {
			skipped++
			continue
		}
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
		_ = s.dbc().QueryRow(
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
				// migrateV056PG, re-created in migrateV068PG as
				// 6-col to match B188.2's intent) + ON CONFLICT
				// DO NOTHING to close the SELECT-then-INSERT race
				// that previously let duplicate rows accumulate.
				// The pre-check SELECT is still useful for the
				// cdnAdded counter (to know if a NEW row was
				// created vs an existing one was hit), but the
				// race is closed by the conflict target.
				//
				// 2026-09-07 (B237.23): conflict target is 6
				// columns (WITH parent_domain) to match the
				// 6-col UNIQUE INDEX on the live DB and the
				// qInsertDeviceRule contract in queries.go.
				// The pre-B237.23 5-col target (B183) was a
				// silent code/index drift: V068 (B232) recreated
				// the index as 6-col but didn't update sync.go,
				// so every INSERT here hit
				// `no unique or exclusion constraint matching`
				// and the `if err != nil { continue }` below
				// silently swallowed it. Net effect: /32 rows
				// for the 15 Cloudflare CIDRs were never
				// created when the marker (cdn:cloudflare:foo)
				// already had rows under a DIFFERENT marker
				// (cdn:cloudflare:discordapp.com); the autoupdate
				// logged `added=0` and the UI's B184 status
				// check saw "no resolved subnets" → ⏳ orange
				// forever (false positive — the rules work,
				// karolina's ApprovedRoutes has the IP).
				ownerName, ownerHost := owners.get(d.userID, d.deviceID)
				tag, err := s.dbc().Exec(
					`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname)
					 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7, $8, $9)
					 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
					d.userID, d.deviceID, d.exitNode, cidr, d.action, d.deviceIP, marker, ownerName, ownerHost)
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
			if _, err := s.dbc().Exec(
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
			// B308: a successful (CDN) resolution counts — the next ticks skip this
			// domain until the interval elapses.
			s.markDomainResolved(d.domain, nowUnix)
			continue
		}

		// Get existing /32 rules for this domain
		existing := map[string]int{} // IP -> rule id
		rows2, eerr := s.dbc().Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32'",
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
		rows3, _ := s.dbc().Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain,'')=$4",
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
			// UNIQUE INDEX device_rules_natural_key_uniq from
			// migrateV056PG, re-created in migrateV068PG as
			// 6-col) instead of the pre-check + INSERT race.
			// The pre-check is preserved for the "shared IP
			// between domains" case (B123 alert UX) — when
			// another domain already added the /32, the
			// conflict target skips silently.
			//
			// 2026-09-07 (B237.23): conflict target is 6
			// columns (WITH parent_domain) to match the
			// 6-col UNIQUE INDEX on the live DB. Pre-B237.23
			// the 5-col target (B183) silently failed every
			// INSERT because V068 (B232) recreated the index
			// as 6-col but didn't update sync.go. With 6-col
			// ON CONFLICT, two parent_domains resolving to
			// the same /32 (e.g. www.harness.io and
			// harness.io both → 44.246.83.163) each get
			// their own row — the B184 status check
			// correctly finds the /32 for the parent_domain
			// it's looking for. See B237.23 entry in
			// AGENTS.md for the full regression analysis.
			ownerName, ownerHost := owners.get(d.userID, d.deviceID)
			tag, ierr := s.dbc().Exec(
				`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname)
				 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7, $8, $9)
				 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
				d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, d.domain, ownerName, ownerHost)
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
			if _, derr := s.dbc().Exec("DELETE FROM device_rules WHERE id=$1", rid); derr == nil {
				removed++
			}
		}

		if len(currentIPs) > 0 || len(all32) > 0 {
			s.logAutoUpdate(d.id, d.domain, added, removed, "")
		}
		// B308: the non-CDN path resolved and reconciled this domain — gate the
		// next resolve. A failed lookup returned early above and deliberately does
		// NOT mark the domain, so an unreachable resolver is retried next tick.
		s.markDomainResolved(d.domain, nowUnix)
	}

	if skipped > 0 {
		log.Printf("auto-updater: %d domain(s) skipped (re-resolve interval %s; the derived rows and the ACL stay put until then)",
			skipped, DomainResolveIntervalLabel(resolveInterval))
	}

	// B276: the rule set just changed (or did not — the derived rows are rewritten
	// on every tick), so ask the only question that matters for the control plane:
	// is headscale serving the policy skygate would generate right now? Live, 8 of
	// the 15 newest rules had NO alias in the policy at all — the ACL was only ever
	// regenerated by an explicit rule/user/device change, so resolved domains and
	// reassigned prefixes silently outran it. Running the comparison here makes the
	// policy converge on its own, without the operator pressing anything, and the
	// equivalence guard means a quiet tick costs one GenerateACL and nothing else.
	//
	// B276.1 runs FIRST so the ACL generated below already covers the devices a
	// user's "all my devices" rules were just extended to; otherwise those rows
	// would wait a whole tick for their grants.
	defer func() {
		// B351: heal the denormalised owner column BEFORE the propagation and the
		// ACL comparison, so the device index, the fan-out label and the generated
		// policy all read a repaired row in the same pass. The UPDATE matches
		// nothing once the table is healed, so a quiet tick pays one statement.
		if n, herr := db.BackfillDeviceRuleUserNames(s.dbc()); herr != nil {
			log.Printf("auto-updater: user_name backfill failed: %v", herr)
		} else if n > 0 {
			log.Printf("auto-updater: backfilled the owner on %d rule row(s)", n)
		}
		// B352 (2026-10-05): the ownership decision is made from the HEALTHY set, and a
		// relay excluded for ONE failed apply (B309) returns to that set the moment it
		// answers — but until now nothing re-ran the decision unless a sync happened to
		// fire after the recovery. Live: karolina timed out once at 19:44, was excluded
		// at 19:54:07, answered again at 19:54:10 (`ssh=ok`), and still owned ZERO of its
		// 251 claimed prefixes 40 minutes later — the assignment table had simply not
		// been recomputed, so the per-CIDR pins stayed on emilia/shardlotta and every
		// device pinned to karolina had no access to its own destinations. Reconciling
		// here makes ownership converge within one tick (5 min) instead of "whenever
		// somebody presses Sync". The pass is cheap: no rows move without a reason.
		if ins, chg, rerr := s.reconcilePrefixOwnership(); rerr != nil {
			log.Printf("prefix-owner: reconcile: %v", rerr)
		} else if ins > 0 || chg > 0 {
			log.Printf("prefix-owner: assignment table updated (inserted=%d changed=%d)", ins, chg)
		}
		if n, perr := s.propagateAllDeviceRules(); perr != nil {
			log.Printf("all-devices: propagation failed: %v", perr)
		} else if n > 0 {
			log.Printf("all-devices: %d rule row(s) added for newly registered devices", n)
		}
		// B298: this is the DERIVED-rule path, so it spends the churn budget, not
		// the 60s ownership one. The rule-set delta it reports is usually a
		// rotating /32 from DNS, and on a file-mode host every apply restarts
		// headscale — a rotating address is worth waiting for, a restart every
		// five minutes is not.
		s.applyACLIfDriftedChurn("skygate-auto-updater",
			fmt.Sprintf("auto-updater tick changed %d rule(s) (added=%d removed=%d)", added+removed, added, removed), true)
	}()

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
	_ = db.AppendExitRuleLog(s.dbc(), db.ExitRuleLogNoVersion, db.ExitRuleActionAutoupdate, detail)
}

// lookupAcceptRoutes returns the per-exit-node Tailscale AcceptRoutes
// preference stored in exit_servers.accept_routes:
//
//	-1 -> --accept-routes=false (nodes that co-host another VPN, e.g. Amnezia-AWG)
//	 0 -> unset, do not change AcceptRoutes on the node
//	 1 -> --accept-routes=true
//
// Lookup is keyed on the node's hostname. Falls back to 0 (do not change)
// if the node is not in exit_servers or the column is missing.
//
// 2026-07-12: Этап 10 part 5 — moved the SELECT to db.LookupExitServerAcceptRoutes
// (which centralises the column name + the no-row fallback to 0).
func (s *Service) lookupAcceptRoutes(nodeHostname string) int {
	if s == nil || s.dbc() == nil || nodeHostname == "" {
		return 0
	}
	accept, _ := db.LookupExitServerAcceptRoutes(s.dbc(), nodeHostname)
	return accept
}
