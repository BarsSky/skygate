package admin

// derp_mapaddr_b510.go — B510 (2026-11-25)
//
// THE CELLULAR-ONLY SPIKE: our own DERP region was the ONE region in the map without an
// address hint, and its name had no AAAA record either.
//
// Operator report: «проблема именно в скачках, и это в основном у мобильных устройств по
// мобильной сети, на вайфае таких проблем нет». That distinction is the diagnosis: a
// Wi-Fi client gets a DIRECT path, so DERP is not in its traffic at all; a cellular client
// is relayed, so anything that resets the relay path is felt as a stalled stream.
//
// Measured on the reference host, all read-only:
//   * `derp.skynas.ru`, `head.skynas.ru`, `skygate.skynas.ru` answered with an A record
//     ONLY — no AAAA, although the host has a public IPv6 and the derper binds it.
//   * the derpmap node we publish for region 900 carried `DERPPort`/`STUNPort` but no
//     `IPv4` and no `IPv6` — verified in the served JSON: ipv4=None ipv6=None.
//   * tailscaled logged `netcheck.runProbe: named node "mow-1" has no v6 address`
//     continuously, ran ~1224 netcheck probes in 6 hours, and re-established DERP
//     connections 96 times in the same window — the trace of a map the client keeps
//     re-evaluating.
// Cellular networks in this region are IPv6-first (many are v6-only behind 464XLAT). The
// default Tailscale regions DO carry IPv6, ours did not, so an IPv6-preferring phone has
// every reason to keep re-probing, sometimes prefer another region, and re-establish the
// relay session — which the user feels as a spike, and only on cellular.
//
// WHAT THIS FILE DOES: the published node now carries the addresses we can actually
// resolve for the hostname (v4 always when present, v6 when present), so the client needs
// no resolution guesswork and the "no v6 address" complaint has a real answer either way;
// and when NO v6 exists the relay is reported, in the journal and on the admin page, as a
// NAMED problem with the exact DNS record to add — instead of an endless netcheck loop
// that nobody attributes to DNS.
//
// It deliberately does NOT invent an IPv6. A wrong hint would make every v6-capable
// client dial an address that does not answer — strictly worse than an honest absence.

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// derpAddressLookupTimeout bounds the DNS resolution that runs while a derpmap is being
// built. The map is generated on request, so this must never become a slow path: on
// timeout the node simply carries no hints (the previous behaviour) instead of making
// headscale wait.
const derpAddressLookupTimeout = 2 * time.Second

// pickDerpAddresses is the pure half: given every address a name resolved to, choose the
// two hints Tailscale's derpmap carries. It ignores link-local and ULA IPv6 (a client
// cannot reach those across the internet) and anything unusable.
func pickDerpAddresses(ips []net.IP) (v4, v6 string) {
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if v := ip.To4(); v != nil {
			if v4 == "" && !v.IsUnspecified() && !v.IsLoopback() {
				v4 = v.String()
			}
			continue
		}
		if v6 == "" && ip.To16() != nil && !ip.IsUnspecified() && !ip.IsLoopback() &&
			!ip.IsLinkLocalUnicast() && !isULAv6(ip) {
			v6 = ip.String()
		}
	}
	return v4, v6
}

// isULAv6 reports whether the address is a unique-local IPv6 (fc00::/7). It is never
// reachable from the internet, so publishing it as a DERP hint would be a lie.
func isULAv6(ip net.IP) bool {
	v6 := ip.To16()
	if v6 == nil || ip.To4() != nil {
		return false
	}
	return v6[0]&0xfe == 0xfc
}

// derpNodeAddresses resolves the hostname and returns the hints, or empty strings when the
// lookup fails. A failure is NOT an error for the map: the node is still published with its
// hostname, exactly as before B510 — clients that can resolve the name lose nothing.
func derpNodeAddresses(host string) (v4, v6 string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), derpAddressLookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return "", ""
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return pickDerpAddresses(ips)
}

// The warning is per host and rate-limited: the derpmap is regenerated on every fetch
// (headscale polls it), so an unguarded log line would bury the journal.
var (
	derpAddrWarnMu   sync.Mutex
	derpAddrWarnLast = map[string]time.Time{}
)

const derpAddrWarnInterval = time.Hour

// noteDerpAddressGap decides whether to warn about a relay that has no IPv6. It returns
// the warning to log (and logs nothing itself) so the caller keeps one log site, and it is
// exported to the package's tests through this indirection.
//
// An IPv4-only relay is legitimate (many VPS have no v6). The problem is that it was
// SILENT: a cellular, IPv6-preferring client kept re-probing the region, and every probe
// surface said "no v6 address" without ever naming the DNS record that would fix it.
func noteDerpAddressGap(host, v4, v6 string) string {
	if host == "" || v6 != "" {
		return ""
	}
	now := time.Now()
	derpAddrWarnMu.Lock()
	last, seen := derpAddrWarnLast[host]
	if seen && now.Sub(last) < derpAddrWarnInterval {
		derpAddrWarnMu.Unlock()
		return ""
	}
	derpAddrWarnLast[host] = now
	derpAddrWarnMu.Unlock()

	have := "its name resolves to no address at all"
	if v4 != "" {
		have = "the name has an A record (" + v4 + ") but no AAAA"
	}
	return "derpmap: relay " + host + " publishes NO IPv6 hint (" + have + ") — " +
		"IPv6-preferring clients (typically cellular, often v6-only behind 464XLAT) cannot probe " +
		"this region natively and keep re-selecting DERP, which shows up as an unstable stream " +
		"on mobile data and not on Wi-Fi. Fix: add an AAAA record for " + host + " pointing at " +
		"the host's public IPv6 (and keep the A record)."
}
