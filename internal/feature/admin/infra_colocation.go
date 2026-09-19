// File: internal/feature/admin/infra_colocation.go
//
// B265 (2026-09-19) — "skygate runs ON the exit node" detection.
//
// WHY THIS EXISTS
//
// skygate manages exit nodes over SSH (internal/headscale/routes.go →
// `tailscale set --advertise-exit-node …`) and reaches headscale and
// the Telegram Bot API through its OWN tailscaled socket (the skygate
// container's `tailscale up` client, or the host's tailscaled on a
// native install).
//
// When the machine that runs skygate is ALSO one of the exit nodes, two
// things change and both are invisible in the UI today:
//
//  1. If that exit node is selected (as the machine's own exit node, or
//     as `telegram.egress_node_id`), skygate's client traffic is routed
//     through a node it is simultaneously supposed to *manage*. A
//     mis-advertised route set or a broken exit path then takes the
//     management plane down with it — you cannot fix the relay through
//     the relay. This is the classic "I rebooted the exit node and lost
//     the panel" trap.
//
//  2. API credentials and egress policy have to be configured FROM THE
//     CLIENT, not from this host: the Telegram Bot API probe the
//     /admin/telegram page runs originates on this machine (and, for the
//     skygate container, uses the host's network stack), so an
//     exit-node/split-routing setup that this host cannot traverse makes
//     the probe fail even though every other device on the tailnet can
//     reach api.telegram.org. The operator must be told to configure
//     that path from a device that can reach it.
//
// The rule is deliberately conservative: we only flag a node when one of
// its addresses or its hostname matches an identity of THIS host. A
// false positive would be noisy but harmless (a warning), whereas a
// false negative is exactly the dangerous case above.

package admin

import (
	"database/sql"
	"log"
	"os"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// ColocationIdentity is one way this skygate host can be identified on
// the tailnet. Source records where the value came from so the warning
// can explain itself ("matched SKYGATE_TS_HOSTNAME").
type ColocationIdentity struct {
	Value  string
	Source string
}

// ExitNodeColocation is one detected overlap between this host and an
// exit-node device.
type ExitNodeColocation struct {
	NodeID       string
	Hostname     string
	MatchedValue string
	MatchedBy    string
}

// ColocationWarningsText is the operator-facing message for
// /admin/telegram (and the boot log). Kept as a pure function so the
// wording is testable and translatable at the call site if needed.
func ExitNodeColocationSummary(cols []ExitNodeColocation) string {
	if len(cols) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString("exit node ")
		b.WriteString(c.Hostname)
		b.WriteString(" (node_id=")
		b.WriteString(c.NodeID)
		b.WriteString(") is this host (matched ")
		b.WriteString(c.MatchedValue)
		b.WriteString(" via ")
		b.WriteString(c.MatchedBy)
		b.WriteString(")")
	}
	return b.String()
}

// hostIdentities collects the values that identify THIS skygate host on
// the tailnet: the configured Tailscale hostname, the operator-supplied
// probe host, and the machine's own hostname. Every source is optional.
//
// Pure function (reads env + os.Hostname through the caller's values) so
// it is unit-testable.
func hostIdentities(cfgHostname, probeHost, osHostname string, extraIPs []string) []ColocationIdentity {
	var out []ColocationIdentity
	seen := map[string]bool{}
	add := func(v, src string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, ColocationIdentity{Value: v, Source: src})
	}
	add(cfgHostname, "SKYGATE_TS_HOSTNAME")
	add(os.Getenv("SKYGATE_TS_HOSTNAME"), "SKYGATE_TS_HOSTNAME")
	add(probeHost, "SKYGATE_DERP_PROBE_HOST")
	add(os.Getenv("SKYGATE_DERP_PROBE_HOST"), "SKYGATE_DERP_PROBE_HOST")
	add(osHostname, "os.Hostname")
	// A tailnet IP that belongs to this host (native installs where
	// tailscaled runs on the host and skygate knows its own address).
	for _, ip := range extraIPs {
		add(ip, "self tailnet IP")
	}
	return out
}

// isColocatedExitNode reports whether `n` is this host, and by which
// identity. Compares case-insensitively on:
//   - any of the node's IP addresses (v4 and v6) against the identities,
//   - the node's hostname / given name against the identities,
//   - the special-case equality of the node hostname with the skygate
//     host's own hostname prefix (`skygate-host`), which is the reserved
//     name skygate gives its own tailnet client (B251).
//
// Pure function.
func isColocatedExitNode(n headscale.NodeView, ids []ColocationIdentity) (ColocationIdentity, bool) {
	names := []string{strings.ToLower(strings.TrimSpace(n.Hostname)), strings.ToLower(strings.TrimSpace(n.GivenName))}
	for _, id := range ids {
		for _, ip := range n.IPAddresses {
			if strings.EqualFold(strings.TrimSpace(ip), id.Value) {
				return id, true
			}
		}
		for _, nm := range names {
			if nm == "" {
				continue
			}
			if nm == id.Value {
				return id, true
			}
			// `skygate-host` is the reserved hostname of the skygate
			// tailnet client (B251). A node named exactly that, or the
			// identity being that reserved name, means "this host".
			if nm == "skygate-host" || id.Value == "skygate-host" {
				return id, true
			}
		}
	}
	return ColocationIdentity{}, false
}

// SanityCheckExitNodeColocation walks the live exit nodes and reports
// the ones that are this host. Read-only; never fails the boot.
//
// `cfgHostname` is the configured Tailscale hostname of this deployment
// (SKYGATE_TS_HOSTNAME); `selfIPs` are any tailnet addresses this
// process knows it owns (may be nil).
//
// Logs one WARN line per overlap. Returns them for callers that want to
// render the warning (e.g. /admin/telegram).
func SanityCheckExitNodeColocation(hs *headscale.Client, cfgHostname string, selfIPs []string) []ExitNodeColocation {
	if hs == nil {
		return nil
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		// Non-fatal: the check is advisory. The next boot (or a page
		// render that calls this) retries.
		return nil
	}
	osHost, _ := os.Hostname()
	ids := hostIdentities(cfgHostname, os.Getenv("SKYGATE_DERP_PROBE_HOST"), osHost, selfIPs)
	var out []ExitNodeColocation
	for _, n := range nodes {
		if !n.IsExitNode && !hasExitTag(n.Tags) {
			continue
		}
		id, ok := isColocatedExitNode(n, ids)
		if !ok {
			continue
		}
		out = append(out, ExitNodeColocation{
			NodeID:       n.ID,
			Hostname:     firstNonEmptyStr(n.Hostname, n.GivenName),
			MatchedValue: id.Value,
			MatchedBy:    id.Source,
		})
	}
	if len(out) > 0 {
		log.Printf("exit-colocation: WARNING skygate runs on the same host as %d exit node(s): %s", len(out), ExitNodeColocationSummary(out))
		log.Printf("exit-colocation: do NOT select a co-located exit node as this machine's own exit node (or as telegram.egress_node_id): a broken relay then takes the management plane down with it, and the /admin/telegram probe can no longer be trusted. Configure Telegram egress from a client that can reach api.telegram.org.")
	}
	return out
}

// SelfExitNodeIdentities returns the identities of THIS host that can be
// derived from skygate's own exit_servers rows: the tailscale IPs and the
// SSH targets of any relay whose hostname is one of the host identities.
//
// Why: a native install (skygate on the same box as a systemd derper +
// tailscaled) has no `SKYGATE_TS_HOSTNAME` match for a relay named
// differently, but its exit_servers row carries the node's own tailnet IP.
// Feeding those IPs into SanityCheckExitNodeColocation makes the match
// work for that shape.
//
// Best-effort: any DB error returns the input unchanged.
func SelfExitNodeIdentities(dbConn *sql.DB, cfgHostname string) []string {
	if dbConn == nil {
		return nil
	}
	osHost, _ := os.Hostname()
	ids := hostIdentities(cfgHostname, os.Getenv("SKYGATE_DERP_PROBE_HOST"), osHost, nil)
	servers, err := db.ListExitServers(dbConn)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range servers {
		host := strings.ToLower(strings.TrimSpace(s.Hostname))
		target := strings.ToLower(strings.TrimSpace(s.SSHTarget))
		matched := false
		for _, id := range ids {
			if host != "" && host == id.Value {
				matched = true
			}
			if target != "" && strings.Contains(target, id.Value) {
				matched = true
			}
		}
		if !matched {
			continue
		}
		for _, ip := range strings.Split(s.TailscaleIP, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				out = append(out, ip)
			}
		}
	}
	return out
}

// hasExitTag reports whether any of the node's tags is the exit-node tag.
// Local helper (the headscale package keeps its own predicate private to
// the NodeView type).
func hasExitTag(tags []string) bool {
	for _, t := range tags {
		if t == "tag:exit-node" {
			return true
		}
	}
	return false
}

// selfHostMatches reports whether an exit_servers row belongs to THIS
// host, given the set of self identities (lowercased). A row matches when
// any of its tailnet IPs is a self identity.
//
// Pure function — used by loadTelegramUIState so the /admin/telegram
// egress card can warn about a co-located relay.
func selfHostMatches(e db.ExitServer, selfSet map[string]bool) bool {
	if len(selfSet) == 0 {
		return false
	}
	for _, ip := range strings.Split(e.TailscaleIP, ",") {
		if selfSet[strings.ToLower(strings.TrimSpace(ip))] {
			return true
		}
	}
	return false
}
