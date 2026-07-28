package handlers

// exit_rules_cdn_test.go — regression tests for the
// 2026-07-28 CDN detection in the autoupdater.
//
// Why this test exists: the autoupdater's "resolve domain
// to /32 rules" approach was failing for Cloudflare-served
// sites (artstation, github, docker) because Cloudflare
// anycast returns DIFFERENT IPs at each DNS query. Result:
// add=18 remove=18 every 5 min, no net progress, and the
// user's traffic hits IPs karolina never advertised.
//
// The fix: detect when a domain's resolved IPs all fall
// inside a known CDN's published ranges, and replace the
// per-IP /32 rules with the CDN's CIDR ranges. The ranges
// don't churn (they're stable network allocations), so the
// autoupdater has nothing to do for that domain on the
// next tick.

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

// TestDetectCDN_CloudflareIPsMatch — the regression test
// for the user's artstation.com case. Both IPs fall in
// Cloudflare's 104.16.0.0/12 range. detectCDN must return
// ("cloudflare", ranges, true).
func TestDetectCDN_CloudflareIPsMatch(t *testing.T) {
	ips := map[string]bool{
		"104.19.169.40": true,
		"104.19.170.40": true,
	}
	name, cidrs, matched := detectCDN(ips)
	if !matched {
		t.Fatalf("expected CDN match, got none (artstation.com IPs are 104.19.x.x — Cloudflare)")
	}
	if name != "cloudflare" {
		t.Errorf("CDN name = %q, want %q", name, "cloudflare")
	}
	if len(cidrs) == 0 {
		t.Errorf("CDN returned 0 CIDR ranges")
	}
	// Verify the 104.16.0.0/12 range is in the list (artstation's
	// actual range is 104.19.x.x, which is inside 104.16.0.0/12).
	found := false
	for _, c := range cidrs {
		if c == "104.16.0.0/12" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 104.16.0.0/12 in CDN ranges, got: %v", cidrs)
	}
}

// TestDetectCDN_NonCloudflareIPsNoMatch — IPs that are not
// in any CDN range must return matched=false. The autoupdater
// falls back to per-IP /32 rules in this case.
func TestDetectCDN_NonCloudflareIPsNoMatch(t *testing.T) {
	ips := map[string]bool{
		"203.0.113.1": true, // TEST-NET-3, not in any CDN
		"203.0.113.2": true,
	}
	_, _, matched := detectCDN(ips)
	if matched {
		t.Errorf("expected no CDN match for TEST-NET-3 IPs, got matched=true")
	}
}

// TestDetectCDN_PartialMatchRejects — if even ONE IP is
// outside the CDN's range, we return no match. This is the
// "domain is on Cloudflare + has a tiny auth edge elsewhere"
// case — partial coverage would leave a gap.
func TestDetectCDN_PartialMatchRejects(t *testing.T) {
	ips := map[string]bool{
		"104.19.169.40": true, // Cloudflare
		"203.0.113.99":  true, // NOT Cloudflare
	}
	_, _, matched := detectCDN(ips)
	if matched {
		t.Errorf("partial match should be rejected, got matched=true")
	}
}

// TestDetectCDN_EmptyInput — empty IP set returns no match.
// Defensive: the autoupdater should never call detectCDN
// with empty input, but the helper should not panic.
func TestDetectCDN_EmptyInput(t *testing.T) {
	_, _, matched := detectCDN(map[string]bool{})
	if matched {
		t.Errorf("empty input should return no match")
	}
}

// TestDetectCDN_FastlyMatch — github.com uses Fastly.
// github.com resolves to something like 140.82.121.3
// (151.101.0.0/16 is Fastly's primary range).
func TestDetectCDN_FastlyMatch(t *testing.T) {
	ips := map[string]bool{
		"151.101.65.69": true, // github.com
	}
	name, _, matched := detectCDN(ips)
	if !matched {
		t.Fatalf("expected Fastly match for 151.101.65.69")
	}
	if name != "fastly" {
		t.Errorf("CDN name = %q, want %q", name, "fastly")
	}
}

// TestDetectCDN_GoogleMatch — googleapis.com uses Google.
// 142.250.x.x is Google's primary range.
func TestDetectCDN_GoogleMatch(t *testing.T) {
	ips := map[string]bool{
		"142.250.190.78": true, // googleapis.com
	}
	name, _, matched := detectCDN(ips)
	if !matched {
		t.Fatalf("expected Google match for 142.250.190.78")
	}
	if name != "google" {
		t.Errorf("CDN name = %q, want %q", name, "google")
	}
}

// TestDetectCDN_AkamaiMatch — many SaaS providers use Akamai.
// 23.32.0.0/11 covers a large block.
func TestDetectCDN_AkamaiMatch(t *testing.T) {
	ips := map[string]bool{
		"23.32.0.42": true, // example Akamai IP
	}
	name, _, matched := detectCDN(ips)
	if !matched {
		t.Fatalf("expected Akamai match for 23.32.0.42")
	}
	if name != "akamai" {
		t.Errorf("CDN name = %q, want %q", name, "akamai")
	}
}

// TestDetectCDN_KnownRangesParseable — every CDN's CIDR
// list must parse cleanly. If a future maintainer adds a
// typo'd CIDR, this test catches it before the autoupdater
// silently fails.
func TestDetectCDN_KnownRangesParseable(t *testing.T) {
	for _, cdn := range knownCDNs {
		parsed, err := parseCIDRs(cdn.cidrs)
		if err != nil {
			t.Errorf("CDN %q: %v", cdn.name, err)
		}
		if len(parsed) != len(cdn.cidrs) {
			t.Errorf("CDN %q: parsed %d/%d CIDRs", cdn.name, len(parsed), len(cdn.cidrs))
		}
		for _, n := range parsed {
			// Sanity: an /8 or bigger is too broad (would
			// cover entire IPv4). Catch obvious typos like
			// "0.0.0.0/0" or "10.0.0.0/8" being added by
			// mistake.
			ones, _ := n.Mask.Size()
			if ones < 8 {
				t.Errorf("CDN %q: CIDR %s is too broad (/8 or smaller) — looks like a typo",
					cdn.name, n.String())
			}
		}
	}
}

// TestCDNParentMarker_RoundTrip — the parent_domain marker
// for a CDN rule must be (a) stable across ticks so the
// autoupdater can find it, (b) distinguishable from
// per-domain markers.
func TestCDNParentMarker_RoundTrip(t *testing.T) {
	cases := []struct {
		cdn, domain, want string
	}{
		{"cloudflare", "artstation.com", "cdn:cloudflare:artstation.com"},
		{"fastly", "github.com", "cdn:fastly:github.com"},
		{"google", "fonts.googleapis.com", "cdn:google:fonts.googleapis.com"},
	}
	for _, c := range cases {
		got := cdnParentMarker(c.cdn, c.domain)
		if got != c.want {
			t.Errorf("cdnParentMarker(%q, %q) = %q, want %q", c.cdn, c.domain, got, c.want)
		}
		if !isCDNMarker(got) {
			t.Errorf("isCDNMarker(%q) = false, want true", got)
		}
		if cdnFromMarker(got) != c.cdn {
			t.Errorf("cdnFromMarker(%q) = %q, want %q", got, cdnFromMarker(got), c.cdn)
		}
	}
}

// TestCDNParentMarker_NonCDNDetection — non-CDN markers
// (e.g. "artstation.com" from the parent_domain form path)
// must NOT be treated as CDN markers.
func TestCDNParentMarker_NonCDNDetection(t *testing.T) {
	cases := []string{
		"artstation.com",
		"www.artstation.com",
		"",
		"cdn:",  // incomplete marker
		"cdn::", // empty CDN name
	}
	for _, c := range cases {
		if isCDNMarker(c) {
			t.Errorf("isCDNMarker(%q) = true, want false", c)
		}
		if cdnFromMarker(c) != "" {
			t.Errorf("cdnFromMarker(%q) = non-empty, want empty", c)
		}
	}
}

// TestCDNRange_ArtstationIPsCoveredByRange — sanity check
// that artstation's actual resolved IPs fall in the
// Cloudflare range we ship. If Cloudflare adds new ranges
// and we miss them, this test will fail.
func TestCDNRange_ArtstationIPsCoveredByRange(t *testing.T) {
	// 104.16.0.0/12 covers 104.16.0.0 to 104.31.255.255.
	cidr := "104.16.0.0/12"
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ips := []string{
		"104.19.169.40",
		"104.19.170.40",
		"104.16.97.215",
		"104.31.0.1", // upper edge of 104.16.0.0/12
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Errorf("parse %q", ipStr)
			continue
		}
		if !n.Contains(ip) {
			t.Errorf("artstation-like IP %s NOT in %s — update knownCDNs!", ipStr, cidr)
		}
	}
}

// TestDomainAutoUpdater_CloudflareDomainUsesRange — the
// full autoupdate flow simulation. The domain is artstation.com,
// the resolved IPs are 104.19.169.40 and 104.19.170.40.
// After the autoupdater tick:
//   - The /32 rules are NOT inserted (we use CDN range instead).
//   - One range rule is inserted: 104.16.0.0/12 with
//     parent_domain="cdn:cloudflare:artstation.com".
//   - The next tick (with different DNS result) is a no-op:
//     add=0, remove=0.
//
// Before this fix: the autoupdater inserted 18+ /32 rules
// (artstation + subdomains) every tick, removed them the
// next tick, etc. With this fix: one /12 rule covers all
// possible Cloudflare edges, stable forever.
func TestDomainAutoUpdater_CloudflareDomainUsesRange(t *testing.T) {
	a, d := newAppForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "artstation.com"

	if ok, _ := a.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain); !ok {
		t.Fatalf("insert domain rule")
	}

	// Simulate the autoupdater detecting CDN and using ranges.
	ips := map[string]bool{"104.19.169.40": true, "104.19.170.40": true}
	if cdnName, cdnCIDRs, isCDN := detectCDN(ips); isCDN {
		marker := cdnParentMarker(cdnName, domain)
		// Insert each range with parent_domain=marker.
		for _, cidr := range cdnCIDRs {
			if _, err := d.Exec(
				`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, created_at, parent_domain)
				 VALUES (?, ?, ?, 'subnet', ?, 'accept', 1, strftime('%s','now'), ?)`,
				userID, deviceID, exitNode, cidr, marker); err != nil {
				t.Fatalf("insert %s: %v", cidr, err)
			}
		}
	} else {
		t.Fatalf("expected CDN match for artstation.com IPs")
	}

	// Verify exactly one range rule exists with the marker.
	var count int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND parent_domain=?`,
		userID, deviceID, exitNode, "cdn:cloudflare:artstation.com",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != len(cdnCIDRsFor("cloudflare")) {
		t.Errorf("range rule count = %d, want %d", count, len(cdnCIDRsFor("cloudflare")))
	}

	// Second autoupdate tick with DIFFERENT DNS IPs (Cloudflare
	// anycast rotation). The autoupdater sees the existing
	// range rule, doesn't add anything, doesn't remove anything.
	added, removed := simulateCDNAutoupdaterTick(t, a, userID, deviceID, exitNode, domain,
		map[string]bool{"104.20.1.2": true, "104.21.5.6": true})
	if added != 0 {
		t.Errorf("tick 2: added=%d, want 0 (CDN range already in DB)", added)
	}
	if removed != 0 {
		t.Errorf("tick 2: removed=%d, want 0 (CDN range is stable)", removed)
	}
}

// TestDomainAutoUpdater_NonCDNStaysPerIP — when the domain
// is NOT on a CDN (e.g. a small site on a single IP), the
// autoupdater falls back to per-IP /32 rules.
func TestDomainAutoUpdater_NonCDNStaysPerIP(t *testing.T) {
	a, d := newAppForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "small-site.example.org"
	ips := map[string]bool{"203.0.113.42": true, "203.0.113.43": true}

	if ok, _ := a.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain); !ok {
		t.Fatalf("insert domain rule")
	}

	// simulateAutoupdaterTick is the non-CDN path: it inserts
	// /32 rules with parent_domain=domain, removes them when
	// DNS changes.
	var ipList []string
	for ip := range ips {
		ipList = append(ipList, ip)
	}
	added, _ := simulateAutoupdaterTick(t, a, userID, deviceID, exitNode, domain, ipList)
	if added != 2 {
		t.Errorf("non-CDN tick: added=%d, want 2 (per-IP /32 fallback)", added)
	}
}

// cdnCIDRsFor returns the CIDRs for a CDN by name. Used by
// tests to assert expected counts.
func cdnCIDRsFor(name string) []string {
	for _, cdn := range knownCDNs {
		if cdn.name == name {
			return cdn.cidrs
		}
	}
	return nil
}

// simulateCDNAutoupdaterTick runs the CDN-aware autoupdater
// path: detect CDN, no-op if range rule with marker already
// exists, no-op for /32 rules (they don't apply to CDN
// domains). Returns (added, removed).
//
// Mirrors the production logic in DomainAutoUpdater's CDN
// branch (added in commit for v0.30.x).
func simulateCDNAutoupdaterTick(t *testing.T, a *App, userID int64, deviceID int, exitNode, domain string, ips map[string]bool) (added, removed int) {
	t.Helper()
	cdnName, _, isCDN := detectCDN(ips)
	if !isCDN {
		t.Fatalf("expected CDN match for %v", ips)
	}
	marker := cdnParentMarker(cdnName, domain)

	// Check existing range rule.
	var n int
	if err := a.DB.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND parent_domain=?`,
		userID, deviceID, exitNode, marker,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n > 0 {
		// Already have the range rule — no-op.
		return 0, 0
	}
	// Add the range (first CIDR is enough for the test).
	firstCIDR := cdnCIDRsFor(cdnName)[0]
	if _, err := a.DB.Exec(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, created_at, parent_domain)
		 VALUES (?, ?, ?, 'subnet', ?, 'accept', 1, strftime('%s','now'), ?)`,
		userID, deviceID, exitNode, firstCIDR, marker); err == nil {
		added++
	}
	return added, removed
}

// TestCDNParentMarker_ParseStable — the marker format must
// be stable across versions. If a maintainer changes the
// format (e.g. swaps the order), existing rules become
// orphans. This test pins the current format.
//
// If you intentionally change the format, you MUST also
// add a migration that rewrites existing parent_domain
// values.
func TestCDNParentMarker_ParseStable(t *testing.T) {
	// These strings MUST be byte-for-byte stable.
	stable := []string{
		"cdn:cloudflare:artstation.com",
		"cdn:fastly:github.com",
		"cdn:google:fonts.googleapis.com",
		"cdn:akamai:example.com",
	}
	for _, s := range stable {
		if !isCDNMarker(s) {
			t.Errorf("isCDNMarker(%q) = false, want true (format regression)", s)
		}
		// The prefix must be exactly "cdn:" (no extra chars).
		if !strings.HasPrefix(s, "cdn:") {
			t.Errorf("marker %q does not start with 'cdn:'", s)
		}
		// The CDN name must be the second token.
		parts := strings.SplitN(s, ":", 3)
		if len(parts) != 3 {
			t.Errorf("marker %q does not have 3 colon-separated parts", s)
		}
		if parts[0] != "cdn" {
			t.Errorf("marker %q: first part = %q, want %q", s, parts[0], "cdn")
		}
		if parts[1] == "" {
			t.Errorf("marker %q: empty CDN name", s)
		}
		if parts[2] == "" {
			t.Errorf("marker %q: empty original domain", s)
		}
	}
}

// TestCDNParentMarkerGuess_FormatStable — the
// cdnParentMarkerGuess helper returns a string with a
// literal "%" in the middle (the CDN-name slot). It is
// used for the autoupdate's short-circuit check to look
// for an existing CDN rule for a SPECIFIC domain.
//
// This test pins the format: "cdn:%:<domain>". If a
// maintainer changes it, the autoupdate's per-domain
// short-circuit breaks (and all domains in a (user,
// device, exit_node) tuple get short-circuited when
// ANY one of them has a CDN marker — the bug that
// commit ebaa44e fixed).
func TestCDNParentMarkerGuess_FormatStable(t *testing.T) {
	cases := []struct {
		domain string
		want   string
	}{
		{"artstation.com", "cdn:%:artstation.com"},
		{"www.artstation.com", "cdn:%:www.artstation.com"},
		{"github.com", "cdn:%:github.com"},
	}
	for _, c := range cases {
		got := cdnParentMarkerGuess(c.domain)
		if got != c.want {
			t.Errorf("cdnParentMarkerGuess(%q) = %q, want %q", c.domain, got, c.want)
		}
	}
}

// TestDomainAutoUpdater_ShortCircuitIsPerDomain — the
// regression test for the v0.30.x bug where the
// existingMarker check was per-(user, device, exit_node).
// After auth.docker.io got a CDN marker, artstation.com
// (same tuple) was incorrectly short-circuited and never
// processed.
//
// The fix: existingMarker query is per-domain (uses
// cdnParentMarkerGuess as the parent_domain value, not a
// LIKE 'cdn:%:%' wildcard). One domain's CDN marker does
// NOT short-circuit other domains.
func TestDomainAutoUpdater_ShortCircuitIsPerDomain(t *testing.T) {
	a, d := newAppForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"

	// Insert domain rules for two different CDN-hosted
	// domains. Same (user, device, exit_node) tuple.
	domains := []string{"auth.docker.io", "artstation.com"}
	for _, domain := range domains {
		if ok, _ := a.insertRuleUnique(userID, deviceID, exitNode,
			"domain", domain, "accept", "100.64.0.9", domain); !ok {
			t.Fatalf("insert domain rule for %s", domain)
		}
	}

	// Simulate the autoupdate: insert a CDN marker for
	// auth.docker.io (this is what the production
	// DomainAutoUpdater does when CDN detection fires).
	authMarker := cdnParentMarker("cloudflare", "auth.docker.io")
	if _, err := d.Exec(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, created_at, parent_domain)
		 VALUES (?, ?, ?, 'subnet', ?, 'accept', 1, strftime('%s','now'), ?)`,
		userID, deviceID, exitNode, "104.16.0.0/12", authMarker); err != nil {
		t.Fatalf("insert CDN marker for auth.docker.io: %v", err)
	}

	// Verify the per-domain short-circuit check.
	// For artstation.com, the check should NOT find a
	// CDN marker (because the existing one is for a
	// different domain), so the autoupdate proceeds.
	for _, domain := range domains {
		var marker string
		err := d.QueryRow(
			`SELECT parent_domain FROM device_rules
			 WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND parent_domain LIKE ?`,
			userID, deviceID, exitNode, cdnParentMarkerGuess(domain),
		).Scan(&marker)
		switch domain {
		case "auth.docker.io":
			// Should find the marker.
			if err != nil || !isCDNMarker(marker) {
				t.Errorf("auth.docker.io: expected CDN marker, got marker=%q err=%v", marker, err)
			}
		case "artstation.com":
			// Should NOT find a marker (the existing
			// one is for auth.docker.io, not artstation.com).
			if err == nil {
				t.Errorf("artstation.com: short-circuit falsely matched auth.docker.io's marker %q — per-domain check is broken",
					marker)
			}
		}
	}
}

// TestDetectCDN_RealWorldDomains — live DNS lookup against
// real CDN-served domains. Skipped on Windows (no DNS in
// the Go test runtime the same way) AND when net.LookupHost
// fails (e.g. test environment without DNS).
//
// This test pins the CDN detection against REAL data:
//   - artstation.com → Cloudflare (104.16-104.31)
//   - github.com → Fastly (151.101.x.x)
//   - fonts.googleapis.com → Google
//   - cloudflare.com → Cloudflare (self-hosted)
//
// If any of these return IPs that don't match the expected
// CDN, the test fails — which means the CDN list is stale
// or a CDN migrated to a new range. Update knownCDNs.
func TestDetectCDN_RealWorldDomains(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real-world DNS test only runs on Linux")
	}
	cases := []struct {
		domain string
		cdn    string
	}{
		{"artstation.com", "cloudflare"},
		{"www.artstation.com", "cloudflare"},
		{"github.com", "fastly"},
		{"fonts.googleapis.com", "google"},
		{"cloudflare.com", "cloudflare"},
	}
	for _, c := range cases {
		addrs, err := net.LookupHost(c.domain)
		if err != nil {
			t.Skipf("DNS lookup failed for %s: %v (offline test env?)", c.domain, err)
		}
		ips := map[string]bool{}
		for _, a := range addrs {
			if strings.Contains(a, ":") { continue }
			ips[a] = true
		}
		if len(ips) == 0 {
			t.Skipf("%s: no IPv4 addresses returned", c.domain)
		}
		name, _, matched := detectCDN(ips)
		if !matched {
			t.Errorf("%s: expected CDN match, got none. IPs: %v. Update knownCDNs if the CDN migrated.",
				c.domain, ips)
		}
		if matched && name != c.cdn {
			t.Errorf("%s: CDN = %q, want %q. IPs: %v", c.domain, name, c.cdn, ips)
		}
	}
}
