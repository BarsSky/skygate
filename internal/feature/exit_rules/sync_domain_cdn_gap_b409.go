// sync_domain_cdn_gap_b409.go — B409 (2026-10-22): a CDN marker must not end the
// conversation.
//
// WHY (measured on the operator's installation, live run on v1.5.153):
//
//	exit_rules.domain_prefix :: 5 of 12 address(es) are stale or missing:
//	  agent.minimax.io → 62.115.253.59 is in no enabled row
//	  cdn-registry-1.docker.io → 54.157.162.28 is in no enabled row
//	  cdn-registry-1.docker.io → 100.60.245.70 is in no enabled row
//
// The rows looked fresh (5 Akamai ranges for one domain, 6 rows for the other), the
// resolver ran that day, and yet the answers were covered by NOTHING. Root cause,
// read straight out of sync_domain.go:
//
//	existingMarker := … parent_domain LIKE 'cdn:%:<domain>' …
//	if isCDNMarker(existingMarker) {
//	    continue        // ← "already handled", BEFORE any DNS lookup
//	}
//
// Once a domain got a CDN marker, the domain was never resolved again — the marker
// was treated as the whole answer. But a CDN publishes a RANGE SET that grows, and a
// domain's answers move between those ranges (Akamai edge 62.115.x, AWS edge for
// cdn-registry-1.docker.io). So the stored set can be perfectly "fresh" (written
// once) while today's answers fall outside it: the rule is enabled, the strings are
// in the DB, and the traffic is not routed.
//
// The fix keeps the good part of the CDN short-circuit (no churn: the published
// ranges are still written once and never rewritten) and adds the missing half: on
// every due pass the domain IS resolved, and any answer NOT covered by the stored
// ranges becomes a /32 row under the SAME marker. Dedup keeps it (the B298 partition
// includes parent_domain), the ACL covers today's answer, and the churn is limited to
// the few addresses that actually moved.
package exit_rules

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// b409Uncovered returns the IPv4 answers that NO stored range contains.
//
// Pure so the containment rule can be tested without a resolver or a database: a
// mistake here would either route nothing (too strict) or hide a genuine gap (too
// loose), and both look identical on the page.
func b409Uncovered(currentIPs map[string]bool, storedRanges []string) []string {
	prefixes := make([]netip.Prefix, 0, len(storedRanges))
	for _, r := range storedRanges {
		if p, err := netip.ParsePrefix(strings.TrimSpace(r)); err == nil {
			prefixes = append(prefixes, p.Masked())
		}
	}
	out := make([]string, 0, len(currentIPs))
	for ip := range currentIPs {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !addr.Is4() {
			continue
		}
		covered := false
		for _, p := range prefixes {
			if p.Contains(addr) {
				covered = true
				break
			}
		}
		if covered && len(prefixes) > 0 {
			continue
		}
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// b409ResolveIPv4 resolves a domain to its IPv4 answers (the same rule the rest of
// the resolver uses: IPv6 is out of scope for the derived rows).
func b409ResolveIPv4(domain string) map[string]bool {
	addrs, err := net.LookupHost(domain)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, a := range addrs {
		if strings.Contains(a, ":") {
			continue
		}
		out[a] = true
	}
	return out
}

// b409StoredRanges loads the ranges already stored under a CDN marker.
func (s *Service) b409StoredRanges(userID int64, deviceID int, exitNode, marker string) []string {
	rows, err := s.dbc().Query(
		`SELECT target_value FROM device_rules
		 WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet'
		   AND COALESCE(parent_domain,'')=$4`, userID, deviceID, exitNode, marker)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

type b409Rule struct {
	id       int
	userID   int64
	deviceID int
	exitNode string
	domain   string
	action   string
	deviceIP string
}

// b409ReconcileGaps is the half the CDN short-circuit was missing: resolve the
// ranges do not contain. Existing /32 gap rows that are no longer answers are
// removed, so a moved address does not leave a stale grant behind.
//
// Returns the number of rows added and removed. It never touches the CDN range rows
// themselves and never writes a /32 that a range already covers.
func (s *Service) b409ReconcileGaps(d b409Rule, marker, ownerName, ownerHost string) (added, removed int) {
	current := b409ResolveIPv4(d.domain)
	if current == nil {
		return 0, 0 // resolver unavailable: not a verdict, retried next tick
	}
	if extra := s.resolveDomainSubdomains(d.domain); extra != nil {
		for ip := range extra {
			current[ip] = true
		}
	}
	stored := s.b409StoredRanges(d.userID, d.deviceID, d.exitNode, marker)
	uncovered := b409Uncovered(current, stored)

	// Existing gap rows under this marker: the ones that are /32 (a range row has a
	// different prefix length, so this cannot touch the CDN ranges).
	existing := map[string]int{}
	if rows, err := s.dbc().Query(
		`SELECT id, target_value FROM device_rules
		 WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet'
		   AND COALESCE(parent_domain,'')=$4 AND target_value LIKE '%/32'`,
		d.userID, d.deviceID, d.exitNode, marker); err == nil {
		for rows.Next() {
			var rid int
			var val string
			if rows.Scan(&rid, &val) == nil {
				existing[strings.TrimSuffix(val, "/32")] = rid
			}
		}
		rows.Close()
	}

	wanted := map[string]bool{}
	for _, ip := range uncovered {
		wanted[ip] = true
		if _, ok := existing[ip]; ok {
			continue
		}
		res, err := s.dbc().Exec(
			`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, group_label)
			 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7, $8, $9,
			         COALESCE((SELECT group_label FROM device_rules
			                    WHERE user_id = $1 AND device_id = $2 AND exit_node_id = $3
			                      AND target_type = 'domain' AND target_value = $10), ''))
			 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
			d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, marker,
			ownerName, ownerHost, d.domain)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	for ip, rid := range existing {
		if wanted[ip] {
			continue
		}
		if _, err := s.dbc().Exec(`DELETE FROM device_rules WHERE id=$1`, rid); err == nil {
			removed++
		}
	}
	return added, removed
}
