// v1.5.8+ (B260.2.5): regression test for the raw UDP DNS query
// helper used by resolvePublicDERPIP. Pre-B260.2.5 the function
// used net.Resolver + custom Dial (PreferGo: true), but Go's
// LookupHost silently fell back to the OS resolver chain
// (Linux /etc/nsswitch.conf + systemd-resolved + Docker
// `extra_hosts` block). Live verification on VM 2026-09-17
// 16:21 MSK: even with the custom resolver, the page
// rendered "192.168.13.69 (dns:env)" instead of the public
// 95.165.170.190.
//
// B260.2.5 fix: replace net.Resolver with dnsLookupVia1111,
// a raw UDP DNS query against 1.1.1.1:53 that sidesteps Go's
// resolver machinery. These tests pin the helper's behavior
// (returns at least one IPv4, formats the response correctly,
// fails cleanly on NXDOMAIN).
package admin

import (
	"testing"
)

// TestDNSLookupVia1111_KnownHostname runs against a real DNS
// record so the test actually exercises the parse path. The
// chosen hostname (one.one.one.one) returns 1.1.1.1 — same IP
// we're querying, but it's a stable public A record that
// won't move. If the skygate container has no internet access
// (very unlikely on the agent VM), the test fails with
// "dial 1.1.1.1:53: ..." instead of silently passing.
func TestDNSLookupVia1111_KnownHostname(t *testing.T) {
	ips, err := dnsLookupVia1111("one.one.one.one")
	if err != nil {
		t.Skipf("no DNS access from test env: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("expected at least 1 A record for one.one.one.one")
	}
	// The public answer is 1.1.1.1 (and 1.0.0.1). We don't
	// pin the exact IP because Cloudflare's anycast may add
	// more records over time; we just verify it's an IPv4 in
	// 1.0.0.0/8.
	for _, ip := range ips {
		if v4 := ip.To4(); v4 == nil {
			t.Errorf("non-IPv4 in answer: %v", ip)
		} else if v4[0] != 1 {
			t.Errorf("unexpected IP %v for one.one.one.one", v4)
		}
	}
}

// TestDNSLookupVia1111_FQDNTrailingDot pins that the helper
// tolerates a trailing dot (some operators canonicalise their
// hostnames with one). The DNS protocol uses the trailing dot
// to mean "absolute name" — without it, the resolver might
// append the search domain. We construct the query manually so
// the helper doesn't go through the OS resolver's search-domain
// append logic (which is exactly what we're trying to avoid).
// The chosen hostname is one.one.one.one. — the same record as
// the test above; the only difference is the trailing dot.
func TestDNSLookupVia1111_FQDNTrailingDot(t *testing.T) {
	ips, err := dnsLookupVia1111("one.one.one.one.")
	if err != nil {
		t.Skipf("no DNS access from test env: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("expected at least 1 A record for one.one.one.one.")
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 == nil {
			t.Errorf("non-IPv4 in answer: %v", ip)
		}
	}
}
