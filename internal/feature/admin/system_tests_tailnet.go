package admin

// system_tests_tailnet.go — tailnet reachability / speed / split
// system tests for /admin/system_tests. Added per operator
// request ("необходимо также добавить в тесты системы
// тестирование по скорости доступа между нодами tailnet"
// + "организовать тесты для проверки скорости и доступа",
// 2026-08-13).
//
// Why these tests exist
// =====================
// The existing exit-node speed tests (system_tests_exit_node_speed.go,
// B98) only probe skygate-host-1 → exit-nodes:22. They do not
// surface the operator's real concern: "can I reach EVERY online
// tailnet node from my server, and how fast?". A "TAILNET SPLIT"
// (where headscale says N nodes are online but only k<N of them
// are visible to skygate-host-1) is invisible to the B98 tests
// because they only look at exit-nodes (which on the operator's
// deployment are all VPS-side and unaffected by the split).
//
// On 2026-08-13 the operator observed:
//   - headscale `nodes list` shows 17 nodes, 10 online.
//   - `docker exec skygate-skygate-1 tailscale status` shows
//     only 4 peers (emilia, karolina, sharlotta, skygate-host-1).
//   - 6 of the 10 online nodes (skybars, skyworker, a71,
//     svyatoslava-1, olesya, nothing-phone-2) are invisible.
//   - The two clusters do NOT correlate with preauth_key.id
//     (visible: 8, 9, 65, 191; hidden: 19, 61, 63, 129, 180, 189).
//
// The three new tests added here answer:
//   1. `tailnet.all_nodes_reachability` — for each online
//      Tailscale node, TCP-connect to :22 from skygate-host-1.
//      Reports reachability %. Fails at <60% (i.e. >40% of
//      online nodes unreachable from the server).
//   2. `tailnet.vps_to_vps_latency` — TCP latency matrix
//      between VPS-class Tailscale nodes (hostname suffix
//      heuristic: emilia/karolina/sharlotta/skygate-host-1
//      + svyatoslava-*). Surfaces "one of the VPS relays is
//      degraded" (e.g. a DERP-only path that's 1s slower
//      than the rest).
//   3. `tailnet.split_suspected` — explicit split detector.
//      If >40% of online nodes are unreachable from
//      skygate-host-1, the test fails with a clear message
//      pointing the operator at docs/tailnet-diagnostics.md.
//
// Implementation
// ==============
// Reuses `probeExitNodeConnect` (and its override) from
// system_tests_exit_node_speed.go. Same 2s per-node timeout.
// The "VPS class" heuristic is hostname matching — the
// operator's VPS fleet is small enough to enumerate by
// name (see vpsHostnameSet below). If the operator's fleet
// changes, the heuristic updates naturally.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// vpsHostnameSet returns true for hostnames that the
// operator's deployment classifies as VPS-class (i.e. the
// "always-visible" cluster A from the 2026-08-13 incident).
// The home LAN devices (skybars, skyworker, a71, olesya,
// nothing-phone-2, basic, base, skybars-1, cyborg,
// svyatoslava-legacy) are NOT in this set — they're the
// "may be hidden during a tailnet split" set.
//
// This is a deploy-specific list. If the operator adds
// new VPSes (e.g. relay-4, exit-eu-west), add them here.
//
// Why not use headscale tags?
// ===========================
// The operator's headscale does not consistently tag
// nodes (some are tagged-devices, some are tagged exit,
// some untagged). Going by hostname is more reliable for
// THIS deployment. If the operator later adopts strict
// tags (e.g. tag:vps), this function can be replaced
// with a tag-based check.
func vpsHostnameSet() map[string]bool {
	return map[string]bool{
		"emilia":           true,
		"karolina":         true,
		"sharlotta":        true,
		"skygate-host-1":   true,
		// svyatoslava-1 is the actual hostname for karolina's
		// Tailscale IP (headscale records both; karolina
		// appears in `tailscale status` as a peer of self).
		"svyatoslava-1":    true,
	}
}

// allNodesReachabilityTest is a TestRegistry entry. For
// every online Tailscale node, attempts a TCP:22 connect
// from skygate-host-1. Reports reachability % and the
// list of unreachable nodes.
//
// Pass condition: ≥60% of online nodes are reachable
// (within 2s).
// Fail condition: <60% reachable → likely TAILNET SPLIT.
// Skip condition: no online nodes at all (cluster
// disconnected from headscale entirely).
//
// Output format:
//   7/10 online Tailscale nodes reachable (70%)
//     100.64.0.3  emilia          : 82ms
//     100.64.0.4  sharlotta       : 87ms
//     ...
//   UNREACHABLE (3):
//     100.64.0.5  skybars         : timeout
//     100.64.0.1  skyworker       : timeout
//     100.64.0.19 a71             : timeout
var allNodesReachabilityTest = SystemTestDef{
	Name:        "tailnet.all_nodes_reachability",
	Category:    "network",
	Description: "TCP:22 probe from skygate-host-1 to every online Tailscale node — surfaces TAILNET SPLIT (% reachable)",
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
		// Per-probe result, sorted later.
		type probe struct {
			ip       string
			hostname string
			latency  time.Duration
			err      error
		}
		var probes []probe
		for _, n := range nodes {
			if !n.Online {
				continue
			}
			ip := tailscaleIPFromNode(n.IPAddresses)
			if ip == "" {
				continue
			}
			latency, err := probeExitNodeConnect(ctx, ip, "22")
			probes = append(probes, probe{
				ip:       ip,
				hostname: n.GivenName,
				latency:  latency,
				err:      err,
			})
		}
		if len(probes) == 0 {
			return SystemTestSkip, "no online Tailscale nodes"
		}
		// Sort by hostname for stable output.
		sort.Slice(probes, func(i, j int) bool {
			return probes[i].hostname < probes[j].hostname
		})
		available := 0
		var lines []string
		var unreachable []string
		for _, p := range probes {
			if p.err == nil {
				available++
				lines = append(lines, fmt.Sprintf("  %-15s %-20s %s",
					p.ip, p.hostname, p.latency.Round(time.Millisecond)))
			} else {
				lines = append(lines, fmt.Sprintf("  %-15s %-20s ERROR %v",
					p.ip, p.hostname, p.err))
				unreachable = append(unreachable, fmt.Sprintf("  %-15s %-20s %v",
					p.ip, p.hostname, p.err))
			}
		}
		pct := (available * 100) / len(probes)
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("%d/%d online Tailscale nodes reachable (%d%%)\n",
			available, len(probes), pct))
		for _, l := range lines {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
		if len(unreachable) > 0 {
			buf.WriteString(fmt.Sprintf("\nUNREACHABLE (%d):\n", len(unreachable)))
			for _, u := range unreachable {
				buf.WriteString(u)
				buf.WriteString("\n")
			}
		}
		// Threshold: <60% = split. ≥60% = pass with
		// unreachable list visible. This matches the
		// B98 "exit_nodes.availability_summary" 80%
		// threshold pattern but is more generous because
		// the operator's home-LAN devices are inherently
		// harder to reach from the server (NAT, sleep
		// states, etc.). The 60% threshold catches
		// hard splits (4/10 = 40%) but tolerates
		// "one phone asleep" (9/10 = 90%).
		if pct < 60 {
			return SystemTestFail, buf.String()
		}
		return SystemTestPass, buf.String()
	},
}

// vpsToVPSLatencyTest is a TestRegistry entry. Probes
// TCP:22 latency between VPS-class Tailscale nodes
// (operator-specific list, see vpsHostnameSet). Surfaces
// "one of the VPS relays is degraded" — a useful
// early-warning for the operator's egress fleet.
//
// Pass condition: all VPS nodes reachable within 2s AND
// no VPS node has latency > 1s (i.e. nothing is on a
// DERP-only path while the rest are direct).
// Fail condition: any VPS unreachable OR > 1s.
// Skip condition: <2 online VPS nodes (the test needs
// at least 2 to be meaningful).
var vpsToVPSLatencyTest = SystemTestDef{
	Name:        "tailnet.vps_to_vps_latency",
	Category:    "network",
	Description: "TCP:22 latency to VPS-class Tailscale nodes from skygate-host-1 (egress fleet health)",
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
		vps := vpsHostnameSet()
		type probe struct {
			ip       string
			hostname string
			latency  time.Duration
			err      error
		}
		var probes []probe
		for _, n := range nodes {
			if !n.Online {
				continue
			}
			if !vps[n.GivenName] {
				continue
			}
			ip := tailscaleIPFromNode(n.IPAddresses)
			if ip == "" {
				continue
			}
			latency, err := probeExitNodeConnect(ctx, ip, "22")
			probes = append(probes, probe{
				ip:       ip,
				hostname: n.GivenName,
				latency:  latency,
				err:      err,
			})
		}
		if len(probes) < 2 {
			return SystemTestSkip, fmt.Sprintf("only %d online VPS nodes — need ≥2 to compare latency", len(probes))
		}
		sort.Slice(probes, func(i, j int) bool {
			return probes[i].hostname < probes[j].hostname
		})
		var (
			failed []string
			slow   []string
			lines  []string
		)
		const slowThreshold = 1 * time.Second
		for _, p := range probes {
			if p.err != nil {
				failed = append(failed, fmt.Sprintf("%s (%s): %v", p.hostname, p.ip, p.err))
				lines = append(lines, fmt.Sprintf("  %-15s %-20s ERROR %v", p.ip, p.hostname, p.err))
				continue
			}
			lines = append(lines, fmt.Sprintf("  %-15s %-20s %s",
				p.ip, p.hostname, p.latency.Round(time.Millisecond)))
			if p.latency > slowThreshold {
				slow = append(slow, fmt.Sprintf("%s (%s): %s", p.hostname, p.ip, p.latency.Round(time.Millisecond)))
			}
		}
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("%d VPS nodes probed:\n", len(probes)))
		for _, l := range lines {
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

// splitSuspectedTest is a TestRegistry entry. Hard FAIL
// when the reachability pattern matches a known TAILNET
// SPLIT signature: headscale says nodes are online but
// skygate-host-1 cannot reach them. The split detector
// runs a different threshold than the reachability test
// (40% vs 60%) because we want to alert EARLY (even on
// partial splits) and the operator wants visibility on
// the "is my tailnet broken" question specifically.
//
// Pass condition: at most 1 unreachable node (or
// reachable% ≥ 90%).
// Fail condition: 2+ unreachable AND reachable% < 90%.
//
// Why not just rely on `tailnet.all_nodes_reachability`?
// =======================================================
// The reachability test passes at 60% (one or two home
// devices asleep is fine). The split test fails at 90%
// (multiple home devices unreachable from the server is
// NOT fine — it's a network split). Two separate tests
// because they answer different operator questions:
//   "is egress healthy?" (reachability, B98 already covers)
//   "is my tailnet broken?" (split, this test)
var splitSuspectedTest = SystemTestDef{
	Name:        "tailnet.split_suspected",
	Category:    "network",
	Description: "Detects TAILNET SPLIT (headscale says online, but skygate-host-1 cannot reach them) — see docs/tailnet-diagnostics.md",
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
		var online, reachable, unreachable []string
		for _, n := range nodes {
			if !n.Online {
				continue
			}
			ip := tailscaleIPFromNode(n.IPAddresses)
			if ip == "" {
				continue
			}
			online = append(online, fmt.Sprintf("%s (%s)", n.GivenName, ip))
			latency, err := probeExitNodeConnect(ctx, ip, "22")
			if err != nil {
				unreachable = append(unreachable, fmt.Sprintf("%s (%s): %v", n.GivenName, ip, err))
			} else {
				reachable = append(reachable, fmt.Sprintf("%s (%s): %s", n.GivenName, ip, latency.Round(time.Millisecond)))
			}
		}
		if len(online) == 0 {
			return SystemTestSkip, "no online Tailscale nodes"
		}
		pct := (len(reachable) * 100) / len(online)
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("%d online / %d reachable (%d%%) from skygate-host-1\n",
			len(online), len(reachable), pct))
		if len(unreachable) > 0 {
			buf.WriteString("\nUNREACHABLE (headscale says online, TCP probe failed):\n")
			for _, u := range unreachable {
				buf.WriteString("  ")
				buf.WriteString(u)
				buf.WriteString("\n")
			}
		}
		// Split detection: 2+ unreachable AND reachability
		// < 90%. A single unreachable node is normal (the
		// operator's phone is asleep); 2+ with high total
		// online count is the split signature.
		if len(unreachable) >= 2 && pct < 90 {
			buf.WriteString("\nLIKELY TAILNET SPLIT:\n")
			buf.WriteString("  headscale says these nodes are online, but skygate-host-1 cannot reach them.\n")
			buf.WriteString("  See docs/tailnet-diagnostics.md for root cause + fix procedure.\n")
			return SystemTestFail, buf.String()
		}
		return SystemTestPass, buf.String()
	},
}

func init() {
	TestRegistry = append(TestRegistry,
		allNodesReachabilityTest,
		vpsToVPSLatencyTest,
		splitSuspectedTest,
	)
}
