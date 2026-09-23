// internal/headscale/local_node_b293.go — B293 (2026-09-23).
//
// "skygate runs ON the exit node" — but as EVIDENCE, not as a guess.
//
// The operator's host `aro` runs headscale, skygate AND the exit node in one
// place, and answered the question directly:
//
//	$ tailscale status --json | jq '{Self: {Host: .Self.HostName, IPs: .Self.TailscaleIPs}}'
//	{"Self": {"Host": "exit-node-vps", "IPs": ["100.64.0.1","fd7a:115c:a1e0::1"]}}
//
// i.e. the local tailscaled IS the relay. Managing it over SSH was therefore
// both pointless (an SSH session from the host to itself, through the tailnet it
// configures) and fragile: it fails exactly when the local tailscaled is the
// thing needing repair, and it demands a key + authorized_keys on the same box.
//
// WHY NOT reuse infra_colocation.go: that check compares *configured identities*
// (`SKYGATE_TS_HOSTNAME`, `SKYGATE_DERP_PROBE_HOST`, `os.Hostname`) with the
// headscale node list. It is the right evidence for a WARNING, but far too weak
// to choose a transport: a hostname collision (or the B265.1 `skygate-host`
// disaster, where a reserved name matched every relay) would silently route a
// REMOTE relay's route updates into the local daemon. Here the decision is made
// from the live daemon's own answer — `Self.TailscaleIPs` against the relay's
// headscale addresses — so a wrong answer is impossible without a duplicated
// tailnet IP, and "I could not ask" degrades to the SSH path rather than to a
// local apply against the wrong node.
package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TailscaleCLI is the local tailscale binary. SKYGATE_TAILSCALE_CLI overrides it
// (an unusual install layout, or a test double).
func TailscaleCLI() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_TAILSCALE_CLI")); p != "" {
		return p
	}
	return "tailscale"
}

// LocalSelf is what the LOCAL tailscaled says about itself.
type LocalSelf struct {
	HostName string
	IPs      []string
	// BackendState is tailscale's own state string ("Running", "Stopped", …).
	// A daemon that is not Running can still be asked who it is, but it cannot
	// apply a route change — callers use this to explain rather than to decide.
	BackendState string
}

// localStatusTimeout bounds the `tailscale status --json` call. The daemon
// answers from local state, so it is normally instant; a hung socket must not
// stall the sync.
const localStatusTimeout = 5 * time.Second

// LocalTailscaleSelf asks the local tailscaled who it is.
//
// The error is deliberately returned rather than swallowed: the caller must be
// able to tell "this is not the relay" from "I could not ask" (the latter keeps
// the SSH path).
func LocalTailscaleSelf() (LocalSelf, error) {
	bin := TailscaleCLI()
	if _, err := exec.LookPath(bin); err != nil {
		return LocalSelf{}, fmt.Errorf("local tailscale CLI %q not found in PATH: %w", bin, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), localStatusTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return LocalSelf{}, fmt.Errorf("local %s status --json: %w", bin, err)
	}
	return ParseLocalTailscaleStatus(out)
}

// LocalInterfaceIPs returns this host's own non-loopback addresses straight from
// the kernel.
//
// B293.1 (2026-09-23) — WHY the daemon is not the only evidence: the native
// install runs skygate as an unprivileged service user, and tailscaled's socket is
// root-owned unless the operator granted `--operator`. `tailscale status --json`
// then fails with a permission error, detection reported "cannot ask the local
// daemon", and the sync stayed on SSH for a relay that is this very host — the
// operator's «команда ничего не дала» exactly. A tailnet address bound on a local
// interface is the same proof as the daemon's own answer (the address exists on
// THIS machine), and reading it needs no privileges at all.
func LocalInterfaceIPs() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("list local interface addresses: %w", err)
	}
	var out []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, ip.String())
	}
	return out, nil
}

// RelayPlacement is the evidence-based answer to "is this relay this host?".
type RelayPlacement struct {
	// Local is true when one of the relay's addresses is owned by this machine.
	Local bool
	// Evidence names what proved it: "local tailscaled" (the daemon's own
	// Self.TailscaleIPs) or "local interface" (the address is bound on a local
	// interface, read without privileges).
	Evidence string
	// MatchedIP is the address that matched.
	MatchedIP string
	// SelfIPs are every address this machine owns, for the self-covering-route
	// guard.
	SelfIPs []string
	// DaemonErr is why the local daemon could not be asked (nil when it answered).
	// Never a reason to stay on SSH by itself — the interface fallback covers it.
	DaemonErr error
}

// localSelfFn / localIfacesFn are the two probes DetectRelayPlacement walks,
// injectable so the evidence chain is unit-tested without a tailscaled daemon.
var (
	localSelfFn   = LocalTailscaleSelf
	localIfacesFn = LocalInterfaceIPs
)

// DetectRelayPlacement decides how this relay is managed.
//
// Order of evidence:
//  1. the live daemon (best: it also identifies the node by name);
//  2. this host's interface addresses (no daemon access needed — the native
//     install's unprivileged service user cannot read a root-owned socket);
//  3. nothing matched → the relay is remote and keeps the SSH transport.
//
// A NEGATIVE answer from the daemon is final (it knows its own address); the
// interface fallback runs only when the daemon could not be asked at all.
func DetectRelayPlacement(nodeIPs []string) RelayPlacement {
	self, daemonErr := localSelfFn()
	if daemonErr == nil {
		ip, ok := IsLocalRelay(self, nodeIPs)
		return RelayPlacement{Local: ok, Evidence: "local tailscaled", MatchedIP: ip, SelfIPs: self.IPs}
	}
	if ips, err := localIfacesFn(); err == nil {
		ip, ok := IsLocalRelay(LocalSelf{IPs: ips}, nodeIPs)
		return RelayPlacement{Local: ok, Evidence: "local interface", MatchedIP: ip, SelfIPs: ips, DaemonErr: daemonErr}
	}
	return RelayPlacement{DaemonErr: daemonErr}
}

// LocalSelfIPs returns this machine's own addresses, preferring the daemon's
// answer and falling back to the interfaces. Used by the self-covering-route guard
// so the loop protection also works for an unprivileged service user.
func LocalSelfIPs() []string {
	if self, err := localSelfFn(); err == nil && len(self.IPs) > 0 {
		return self.IPs
	}
	ips, err := localIfacesFn()
	if err != nil {
		return nil
	}
	return ips
}

// ParseLocalTailscaleStatus decodes the fields of `tailscale status --json` this
// package needs. Pure, so the shape is pinned by a unit test instead of by a
// live daemon.
func ParseLocalTailscaleStatus(raw []byte) (LocalSelf, error) {
	var doc struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			HostName     string   `json:"HostName"`
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return LocalSelf{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	self := LocalSelf{
		HostName:     strings.TrimSpace(doc.Self.HostName),
		BackendState: strings.TrimSpace(doc.BackendState),
	}
	if self.HostName == "" {
		// Older/edge shapes carry only DNSName ("host.tailnet.ts.net.").
		self.HostName = strings.TrimSuffix(strings.TrimSpace(doc.Self.DNSName), ".")
	}
	for _, ip := range doc.Self.TailscaleIPs {
		if ip = strings.TrimSpace(ip); ip != "" {
			self.IPs = append(self.IPs, ip)
		}
	}
	return self, nil
}

// IsLocalRelay reports whether `nodeIPs` (the addresses headscale reports for the
// relay) include an address this host's own tailscaled owns. The second return is
// the matching address, so the caller can show its evidence.
//
// Exact address equality only: tailnet addresses are unique per node, so a match
// is proof, while a hostname comparison is not (see the file comment).
func IsLocalRelay(self LocalSelf, nodeIPs []string) (string, bool) {
	for _, mine := range self.IPs {
		m := strings.TrimSpace(mine)
		if m == "" {
			continue
		}
		for _, theirs := range nodeIPs {
			if strings.EqualFold(strings.TrimSpace(theirs), m) {
				return m, true
			}
		}
	}
	return "", false
}

// SelfCoveringRoutes splits subnet routes into (kept, skipped) where a skipped
// route CONTAINS one of this host's own addresses.
//
// WHY (B293, the co-location footgun): `tailscale set --advertise-routes=` on the
// relay that IS this host means the host starts advertising a network it sits
// inside. Clients then reach that network through the tunnel — including, from
// the host itself, its own LAN peers, which is the documented
// "500–1700 ms to a LAN peer that should be ~1 ms" route loop
// (docs/networking.md, docs/LESSONS.md L-45). For a REMOTE relay the same rule is
// the operator's responsibility; for a co-located one skygate is the one
// configuring it, so it refuses the route and reports it.
//
// The exit-node base routes (0.0.0.0/0, ::/0) are exempt: they legitimately cover
// every address, they are what makes the node an exit node, and tailscale does not
// install an exit route into the advertising node itself.
func SelfCoveringRoutes(routes []string, selfIPs []string) (kept, skipped []string) {
	parsed := make([]net.IP, 0, len(selfIPs))
	for _, s := range selfIPs {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
			parsed = append(parsed, ip)
		}
	}
	for _, r := range routes {
		trimmed := strings.TrimSpace(r)
		if trimmed == "" {
			continue
		}
		if isExitNodeBaseRoute(trimmed) {
			kept = append(kept, trimmed)
			continue
		}
		_, netw, err := net.ParseCIDR(trimmed)
		if err != nil || netw == nil {
			kept = append(kept, trimmed) // not a CIDR we can reason about — leave it
			continue
		}
		containsSelf := false
		for _, ip := range parsed {
			if netw.Contains(ip) {
				containsSelf = true
				break
			}
		}
		if containsSelf {
			skipped = append(skipped, trimmed)
			continue
		}
		kept = append(kept, trimmed)
	}
	return kept, skipped
}

// isExitNodeBaseRoute reports whether r is one of the two exit-node base routes.
func isExitNodeBaseRoute(r string) bool {
	for _, base := range ExitNodeBaseRoutes {
		if r == base {
			return true
		}
	}
	return false
}
