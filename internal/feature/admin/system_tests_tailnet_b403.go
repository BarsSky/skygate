// system_tests_tailnet_b403.go — B403 (2026-10-20): a probe must say WHAT it measured.
//
// WHY. Measured on the operator's installation the same day:
//
//	tailnet.all_nodes_reachability :: 3/6 probable Tailscale nodes reachable (50%)
//	  100.64.0.7  cyborg          ERROR dial tcp 100.64.0.7:18022: i/o timeout
//	  100.64.0.8  skybars-secure  ERROR dial tcp 100.64.0.8:18022: i/o timeout
//	  100.64.0.20 svyatoslava     ERROR dial tcp 100.64.0.20:18022: i/o timeout
//
//	tailnet.split_suspected :: 6 online / 3 reachable (50%)
//
// and the page then said «LIKELY TAILNET SPLIT … see docs/tailnet-diagnostics.md».
// Two things were wrong with that verdict, and neither is about the tailnet:
//
//  1. A TCP connect that is REFUSED proves the path works — the host answered. Only a
//     TIMEOUT (or "no route") leaves the path unproven. The probe treated every error
//     as "unreachable", so a host with the port closed was indistinguishable from a
//     host behind a broken tunnel.
//  2. SSH is not a tailnet primitive. skygate's SSH contract exists for the hosts it
//     MANAGES — the relays and gateway hosts carrying `tag:dev-infra-*` (it configures
//     their advertised routes over SSH). A phone, a laptop or a Windows box never runs
//     sshd, so probing them measures nothing and their failure says nothing about the
//     tailnet. All three "unreachable" nodes above are devices; the one that IS infra
//     (svyatoslava) times out on 18022 while its cluster heartbeat arrives every 30 s —
//     which is a fact about sshd on that host, not about the tunnel.
//
// So the probe now answers three separate questions and says which one it answered:
//
//	open      — a connect completed (SSH is listening)
//	closed    — the host answered and refused: the PATH is proven, sshd is not there
//	filtered  — timeout / no route: the path is NOT proven
//
// and only nodes whose SSH is skygate's own contract are probed at all.
package admin

import (
	"context"
	"strings"
	"time"

	"skygate/internal/headscale"
)

// TailnetProbeVerdict is what a TCP probe actually established.
type TailnetProbeVerdict string

const (
	// TailnetProbeOpen — a connect completed within the timeout.
	TailnetProbeOpen TailnetProbeVerdict = "open"
	// TailnetProbeClosed — the peer answered and refused: the PATH WORKS, the service
	// is not listening. This is a MEASUREMENT of the tailnet, not a failure of it.
	TailnetProbeClosed TailnetProbeVerdict = "closed"
	// TailnetProbeFiltered — timeout / no route / unknown: the path did not prove
	// itself. Only this verdict may be reported as a reachability failure.
	TailnetProbeFiltered TailnetProbeVerdict = "filtered"
)

// TailnetProbeReachable reports whether the verdict PROVES the path works.
func (v TailnetProbeVerdict) Reachable() bool {
	return v == TailnetProbeOpen || v == TailnetProbeClosed
}

// classifyTailnetProbeError maps one connect attempt's error onto a verdict.
//
// The distinction is the whole point of B403: "connection refused" is the kernel
// telling us the packet arrived and was answered, so the tunnel carried it. A timeout
// is the ABSENCE of an answer, which could be the tunnel, a host firewall or a device
// asleep — and therefore may not be blamed on the tailnet without more evidence.
func classifyTailnetProbeError(err error) TailnetProbeVerdict {
	if err == nil {
		return TailnetProbeOpen
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"):
		return TailnetProbeClosed
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"),
		strings.Contains(msg, "network is down"):
		return TailnetProbeFiltered
	}
	// An error we cannot read is not evidence of a working path.
	return TailnetProbeFiltered
}

// probeTailnetNodeVerdict probes the candidate ports and returns the strongest thing it
// learned: an open port wins, then a refusal (the path is proven), then "filtered".
func probeTailnetNodeVerdict(ctx context.Context, host string) (TailnetProbeVerdict, time.Duration, string, error) {
	var lastErr error
	refused := false
	refusedPort := ""
	for _, port := range tailnetProbePorts {
		latency, err := probeExitNodeConnect(ctx, host, port)
		if err == nil {
			return TailnetProbeOpen, latency, port, nil
		}
		lastErr = err
		if classifyTailnetProbeError(err) == TailnetProbeClosed {
			refused = true
			if refusedPort == "" {
				refusedPort = port
			}
		}
	}
	if refused {
		// The host answered on at least one port: the path is proven even though no
		// SSH service is listening there.
		return TailnetProbeClosed, 0, refusedPort, lastErr
	}
	return TailnetProbeFiltered, 0, "", lastErr
}

// tailnetNodeIsInfra reports whether this node's SSH is skygate's own contract.
//
// The technical tag is what makes a host manageable by skygate: relays receive their
// advertised routes over SSH, and the gateway host is skygate's own. Everything else
// is a user's device, and a device without sshd is not a tailnet fault.
func tailnetNodeIsInfra(n headscale.NodeView) bool {
	for _, tag := range n.Tags {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "tag:dev-infra-") {
			return true
		}
	}
	return false
}

// tailnetProbeTargets splits the online nodes into "SSH is part of the contract"
// (probed) and "device, SSH not expected" (counted, never failed).
//
// Pure so the split can be tested without a network or a database: the two classes are
// the core of B403 and a mis-assignment would silently reintroduce the false split.
func tailnetProbeTargets(nodes []headscale.NodeView, skip map[string]bool) (infra, devices []headscale.NodeView) {
	for _, n := range nodes {
		if !n.Online {
			continue
		}
		if skip != nil && skip[n.GivenName] {
			continue
		}
		if tailscaleIPFromNode(n.IPAddresses) == "" {
			continue
		}
		if tailnetNodeIsInfra(n) {
			infra = append(infra, n)
			continue
		}
		devices = append(devices, n)
	}
	return infra, devices
}
