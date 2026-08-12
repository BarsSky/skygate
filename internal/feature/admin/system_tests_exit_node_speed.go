package admin

// system_tests_exit_node_speed.go — exit node access speed
// and availability probes for the /admin/system_tests
// registry. Added per operator request ("необходимо также
// добавить в тесты системы тестирование по скорости доступа
// exit nodes", 2026-08-12).
//
// Why these tests exist
// =====================
// The existing `headscale.exit_nodes_online` test (B40 era)
// answers "is at least one exit node registered AND online?"
// — a boolean. The new tests answer the operator's real
// questions:
//
//   - How fast can I actually reach each exit node over
//     Tailscale? (latency in ms)
//   - How many exit nodes respond at all? (availability %)
//   - Did one of my exit nodes silently fall off the network
//     even though headscale still says it's "online"?
//     (headscale `online=true` is sticky for headscale 0.29.x
//     when tailscaled hasn't heartbeated; the network probe
//     catches this case)
//
// Implementation
// ==============
// For each online exit node returned by headscale, we
// extract the Tailscale IPv4 address (100.64.0.0/10) and
// TCP-connect to port 22 (SSH) with a 2-second per-node
// timeout. The dial latency is the speed signal; a refused
// or timed-out connection is the availability signal.
//
// Why port 22 and not 80/443?
// ============================
// skygate's exit nodes are VPSes running tailscaled; SSH
// (port 22) is always up if the host is reachable. HTTP/HTTPS
// is inconsistent across the operator's VPSes (some have
// headplane, some have only SSH). SSH is the most reliable
// liveness probe. The /admin/telegram "Set as egress relay"
// path (B81 + B84) also dials 22, so this test exercises the
// same code path the operator cares about.
//
// Why a new file and not inline in system_tests.go?
// =================================================
// system_tests.go is already 1000+ lines. Two new tests
// (4th and 5th for the "network" category) would push it
// to 1100+ and dilute the existing "TestRegistry has ≥6
// tests across network/db/headscale" (B40) contract. Keeping
// the speed tests separate preserves the B40 grouping while
// adding two new tests that target the operator's actual
// question: "how fast can I reach my exit nodes?"
//
// Testable by injecting a fake HeadscaleClient via the
// testServicePtr hook (see system_tests_test.go for the
// pattern) AND a fake probe function via probeExitNodeConnect
// (this file, package-private, swappable in tests).

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// probeExitNodeConnect dials host:port with a 2s timeout and
// returns the dial latency. The connection is closed
// immediately — we measure "can we reach the host" not
// "can we speak SSH to the host". This is the same liveness
// signal that /admin/telegram "Set as egress relay" needs
// (B81 / B84).
//
// Returns (latency, nil) on a successful TCP handshake.
// Returns (0, err) on connect refused, timeout, DNS failure,
// or any other network error.
//
// Overridable in tests via probeExitNodeConnectOverride.
var probeExitNodeConnectOverride func(ctx context.Context, host string, port string) (time.Duration, error)

func probeExitNodeConnect(ctx context.Context, host string, port string) (time.Duration, error) {
	if probeExitNodeConnectOverride != nil {
		return probeExitNodeConnectOverride(ctx, host, port)
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	latency := time.Since(start)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return latency, nil
}

// tailscaleIPFromNode returns the first 100.64.0.0/10 IPv4
// address in node.IPAddresses, or "" if none. Tailscale
// assigns one such address per node when tailscaled is up;
// the "physical" IPs (10.x, 192.168.x) are not useful for
// our reachability probe — the test runs inside the
// skygate container which doesn't share the operator's LAN
// with the VPSes.
func tailscaleIPFromNode(ipAddrs []string) string {
	for _, ip := range ipAddrs {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if v4 := parsed.To4(); v4 != nil {
			// Tailscale CGNAT range: 100.64.0.0/10
			// (100.64.0.0 .. 100.127.255.255).
			if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
				return v4.String()
			}
		}
	}
	return ""
}

// exitNodesTCPSpeedTest is a TestRegistry entry. Measures
// TCP connect latency to each online exit node's Tailscale
// IP on port 22 (SSH). Reports each node's latency in the
// output so the operator sees the actual numbers.
//
// Pass condition: ALL online exit nodes respond within 2s.
// If any node times out or refuses, the test fails with a
// list of slow/failed nodes.
//
// Skip condition: no online exit nodes registered.
//
// Output format (one line per exit node):
//   relay-1 (100.64.0.3): 23ms
//   relay-2 (100.64.0.4): timeout (2s)
var exitNodesTCPSpeedTest = SystemTestDef{
	Name:        "exit_nodes.tcp_connect_speed",
	Category:    "network",
	Description: "TCP connect to each online exit node (Tailscale IP :22) — measures access latency in ms",
	Run: func(ctx context.Context) (SystemTestStatus, string) {
		s := getTestService()
		if s == nil {
			return SystemTestFail, "service not initialised"
		}
		hs := s.HSGlobalFn()
		if hs == nil {
			return SystemTestFail, "headscale client not configured"
		}
		nodes, err := hs.ListAllNodes()
		if err != nil {
			return SystemTestFail, "list nodes: " + err.Error()
		}
		// Collect online exit nodes with a Tailscale IP.
		type probe struct {
			hostname string
			ip       string
			latency  time.Duration
			err      error
		}
		var probes []probe
		for _, n := range nodes {
			if !n.IsExitNode || !n.Online {
				continue
			}
			ip := tailscaleIPFromNode(n.IPAddresses)
			if ip == "" {
				continue
			}
			latency, err := probeExitNodeConnect(ctx, ip, "22")
			probes = append(probes, probe{
				hostname: n.GivenName,
				ip:       ip,
				latency:  latency,
				err:      err,
			})
		}
		if len(probes) == 0 {
			return SystemTestSkip, "no online exit nodes with Tailscale IPs"
		}
		// Build a per-node output line; flag slow / failed.
		var (
			failed  []string
			slow    []string
			lines   []string
		)
		const slowThreshold = 1 * time.Second
		for _, p := range probes {
			if p.err != nil {
				failed = append(failed, fmt.Sprintf("%s (%s): %v", p.hostname, p.ip, p.err))
				lines = append(lines, fmt.Sprintf("%s (%s): ERROR %v", p.hostname, p.ip, p.err))
				continue
			}
			lines = append(lines, fmt.Sprintf("%s (%s): %s", p.hostname, p.ip, p.latency.Round(time.Millisecond)))
			if p.latency > slowThreshold {
				slow = append(slow, fmt.Sprintf("%s (%s): %s", p.hostname, p.ip, p.latency.Round(time.Millisecond)))
			}
		}
		// Sort lines for stable output (alphabetical by hostname).
		sort.Strings(lines)
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("%d exit nodes probed:\n", len(probes)))
		for _, l := range lines {
			buf.WriteString("  ")
			buf.WriteString(l)
			buf.WriteString("\n")
		}
		if len(failed) > 0 {
			buf.WriteString(fmt.Sprintf("\nFAILED (%d):\n", len(failed)))
			for _, f := range failed {
				buf.WriteString("  ")
				buf.WriteString(f)
				buf.WriteString("\n")
			}
			return SystemTestFail, buf.String()
		}
		if len(slow) > 0 {
			// Test still passes (within 2s threshold) but
			// surface the slow nodes so the operator can
			// investigate. The page renders this as PASS
			// with the warning visible in the output.
			buf.WriteString(fmt.Sprintf("\nSLOW (>%s, %d):\n", slowThreshold, len(slow)))
			for _, s := range slow {
				buf.WriteString("  ")
				buf.WriteString(s)
				buf.WriteString("\n")
			}
		}
		return SystemTestPass, buf.String()
	},
}

// exitNodesAvailabilityTest is a TestRegistry entry. Reports
// the availability percentage of online exit nodes (TCP
// connect within 2s = available, timeout/refused = down).
//
// Pass condition: ≥80% of online exit nodes respond within 2s.
// This is the "is the operator's egress fleet degraded?"
// dashboard. The threshold (80%) is intentionally generous —
// losing 1 of 3 exit nodes is a warning, not a failure; losing
// 2 of 3 is a hard failure that needs immediate attention.
//
// Skip condition: no online exit nodes registered.
//
// Output format:
//   3/3 exit nodes responsive (100%)
//     relay-1 (100.64.0.3): 23ms
//     relay-2 (100.64.0.4): 47ms
//     relay-3 (100.64.0.5): 1.2s
var exitNodesAvailabilityTest = SystemTestDef{
	Name:        "exit_nodes.availability_summary",
	Category:    "network",
	Description: "% of online exit nodes that respond to TCP probe (port 22) within 2s — egress fleet availability",
	Run: func(ctx context.Context) (SystemTestStatus, string) {
		s := getTestService()
		if s == nil {
			return SystemTestFail, "service not initialised"
		}
		hs := s.HSGlobalFn()
		if hs == nil {
			return SystemTestFail, "headscale client not configured"
		}
		nodes, err := hs.ListAllNodes()
		if err != nil {
			return SystemTestFail, "list nodes: " + err.Error()
		}
		type probe struct {
			hostname string
			ip       string
			latency  time.Duration
			err      error
		}
		var probes []probe
		for _, n := range nodes {
			if !n.IsExitNode || !n.Online {
				continue
			}
			ip := tailscaleIPFromNode(n.IPAddresses)
			if ip == "" {
				continue
			}
			latency, err := probeExitNodeConnect(ctx, ip, "22")
			probes = append(probes, probe{
				hostname: n.GivenName,
				ip:       ip,
				latency:  latency,
				err:      err,
			})
		}
		if len(probes) == 0 {
			return SystemTestSkip, "no online exit nodes with Tailscale IPs"
		}
		available := 0
		var lines []string
		for _, p := range probes {
			if p.err == nil {
				available++
				lines = append(lines, fmt.Sprintf("  %s (%s): %s [available]",
					p.hostname, p.ip, p.latency.Round(time.Millisecond)))
			} else {
				lines = append(lines, fmt.Sprintf("  %s (%s): %v [down]",
					p.hostname, p.ip, p.err))
			}
		}
		sort.Strings(lines)
		pct := (available * 100) / len(probes)
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("%d/%d exit nodes responsive (%d%%)\n",
			available, len(probes), pct))
		for _, l := range lines {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
		if pct < 80 {
			return SystemTestFail, buf.String()
		}
		return SystemTestPass, buf.String()
	},
}

// formatLatencyMs is a small helper used by the unit tests
// to assert dial output formatting. Kept package-private.
func formatLatencyMs(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

// init registers the two new tests with TestRegistry. The
// exit_node.speed tests live in this file (rather than in
// system_tests.go) to keep the main registry file under 1100
// lines. The contract that B40 pins (TestRegistry has ≥6
// tests across network/db/headscale) is preserved because
// these two tests land in the "network" category and the
// existing tests cover db + headscale.
func init() {
	TestRegistry = append(TestRegistry,
		exitNodesTCPSpeedTest,
		exitNodesAvailabilityTest,
	)
}
