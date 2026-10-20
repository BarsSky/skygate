// relaykind.go — B401 (2026-10-20): one answer to "what IS this relay row?".
//
// WHY. The operator's relay list showed three rows and asked «в релеи висят еще два
// не рабочих derp — что это и к чему». Both were real rows in `derp_relays` and
// neither is a usable relay node:
//
//	https://derp.skynas.ru:8443                        — the same host on the LAN-direct
//	                                                     HTTP port (derper-in-docker
//	                                                     migration, B260.2)
//	https://controlplane.tailscale.com/derpmap/default — a DERP MAP DOCUMENT, copied
//	                                                     from the legacy
//	                                                     `derp.external_urls` by
//	                                                     AutoMigrateDerpRelays (B265)
//
// Three places already had their own opinion about this — the map PUBLISHER
// (admin.isDerpMapURL), the health PROBER (derphealth.urlIsRelayNode) and the page's
// per-row "why is this not published" reason — and they were written at three
// different times. This file is the one classifier they can share: a URL is either a
// relay node, a derpmap document, or a path that is not the DERP protocol, and the
// page can therefore SAY which one instead of leaving the operator to guess.
package derphealth

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// RelayKind classifies one stored relay row.
type RelayKind string

const (
	// RelayKindNode is a real DERP endpoint: no path, /derp, or one of derper's
	// probe paths, on the conventional port.
	RelayKindNode RelayKind = "node"
	// RelayKindDerpMap is a public DERP MAP DOCUMENT (…/derpmap/default). headscale
	// merges the public map itself, so such a row is never a relay: it cannot be
	// dialled as one and must never be advertised.
	RelayKindDerpMap RelayKind = "derpmap"
	// RelayKindOtherPath is a URL on a path the DERP protocol does not serve.
	RelayKindOtherPath RelayKind = "other_path"
)

// ClassifyRelayURL answers the kind of a stored relay URL. It never guesses from the
// hostname: an unparseable or host-less URL is RelayKindOtherPath with the reason.
func ClassifyRelayURL(rawURL string) (RelayKind, string) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return RelayKindOtherPath, "empty url"
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return RelayKindOtherPath, "unparseable or host-less url"
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/derpmap") || strings.Contains(u.Path, "/derpmap/") {
		return RelayKindDerpMap, "a DERP map document, not a relay node"
	}
	switch path {
	case "", "/derp", "/derp/probe", "/derp/latency-check":
		return RelayKindNode, ""
	}
	return RelayKindOtherPath, "path " + path + " is not served by the DERP protocol"
}

// RelayPort returns the port a row dials (443 when the URL names none or is broken).
func RelayPort(rawURL string) int {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return 443
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil && port != "" {
		if p, perr := strconv.Atoi(port); perr == nil {
			return p
		}
	}
	return 443
}

// RelayHost returns the HOSTNAME of a row's URL (no port), lowercased.
func RelayHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		return strings.ToLower(strings.TrimSpace(h))
	}
	return strings.ToLower(strings.TrimSpace(u.Host))
}

// AltPortKind says whether a row is a SECOND entry for a host that already has a
// conventional-port row — the `:8443` shape. It is not a defect (derper publishes a
// LAN-direct HTTP port for clients inside the network), but it is not a second relay
// either, and the list must not present it as one.
//
// The decision is made from the DATA (same host, another row, other port), never from
// the notes text, so a hand-written note cannot flip it.
type AltPortKind struct {
	IsAltPort bool
	SameHost  string // the hostname both rows share
	OtherPort int    // the port of the conventional row for that host
}

// ClassifyAltPort decides whether this row duplicates another row's host on a
// different port. `others` is every OTHER row's URL (hostname + port are derived).
func ClassifyAltPort(rawURL string, others []string) AltPortKind {
	host := RelayHost(rawURL)
	if host == "" {
		return AltPortKind{}
	}
	port := RelayPort(rawURL)
	for _, o := range others {
		if RelayHost(o) != host {
			continue
		}
		if op := RelayPort(o); op != port {
			return AltPortKind{IsAltPort: true, SameHost: host, OtherPort: op}
		}
	}
	return AltPortKind{}
}
