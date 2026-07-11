package handlers

// handlers_derp_classify.go — classification of derper peer connections
// (relay / admin WebSocket / LAN / local / unknown) and per-kind
// aggregation for the dashboard hero badges. Pure helpers, no I/O.
//
// Extracted from handlers_derp.go (Этап 8).

import "net"

// derpPeerNPM is the IP of Nginx Proxy Manager, which keeps persistent
// WebSocket connections to the derper for the /admin/derp page.
const derpPeerNPM = "192.168.13.67"

var (
	derpTailscaleNet = net.IPNet{IP: net.ParseIP("100.64.0.0").To4(), Mask: net.CIDRMask(10, 32)}
	derpLANNet       = net.IPNet{IP: net.ParseIP("192.168.13.0").To4(), Mask: net.CIDRMask(24, 32)}
)

// classifyDerpPeer labels a connection source.
//   ws_relay - Tailscale client (100.64.0.0/10 or any public IP hitting derper)
//   ws_admin - Nginx Proxy Manager WebSocket pool (192.168.13.67)
//   lan      - other LAN client (192.168.13.0/24)
//   local    - loopback (already filtered by the snapshot script)
//   unknown  - anything else
func classifyDerpPeer(ip string) string {
	if ip == derpPeerNPM {
		return "ws_admin"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if parsed.IsLoopback() {
		return "local"
	}
	if derpTailscaleNet.Contains(parsed) {
		return "ws_relay"
	}
	if derpLANNet.Contains(parsed) {
		return "lan"
	}
	if !parsed.IsPrivate() {
		return "ws_relay"
	}
	return "unknown"
}

// classifyDerpPeers fills the Kind field in-place; returns the same slice
// for chaining.
func classifyDerpPeers(peers []DerpPeer) []DerpPeer {
	for i := range peers {
		if peers[i].Kind == "" {
			peers[i].Kind = classifyDerpPeer(peers[i].IP)
		}
	}
	return peers
}

// summarizeDerpPeers counts connections per kind for the dashboard hero.
// Always returns a non-nil pointer so the template can check per-kind
// counts and decide whether to show "derper: N conn (transient)" when
// ss sees zero connections but derper reports some.
func summarizeDerpPeers(peers []DerpPeer) *ConnSummary {
	s := &ConnSummary{}
	for _, p := range peers {
		switch p.Kind {
		case "ws_relay":
			s.Relay++
		case "ws_admin":
			s.Admin++
		case "lan":
			s.LAN++
		case "self":
			s.Self++
		default:
			s.Other++
		}
	}
	return s
}
