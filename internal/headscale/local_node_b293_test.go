// internal/headscale/local_node_b293_test.go — B293 (2026-09-23).
//
// The operator's `aro` answered the question directly:
//
//	$ tailscale status --json | jq '{Self: {Host: .Self.HostName, IPs: .Self.TailscaleIPs}}'
//	{"Self": {"Host": "exit-node-vps", "IPs": ["100.64.0.1","fd7a:115c:a1e0::1"]}}
//
// — the local tailscaled IS the exit node, so the routes must be applied locally
// instead of over SSH. These tests pin the evidence rule (address equality, never
// a hostname guess) and the co-location footgun guard.
package headscale

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLocalTailscaleStatus_B293(t *testing.T) {
	// The live `aro` shape, verbatim.
	live := []byte(`{
	  "BackendState": "Running",
	  "Self": {
	    "HostName": "exit-node-vps",
	    "DNSName": "exit-node-vps.tailnet.example.ts.net.",
	    "TailscaleIPs": ["100.64.0.1", "fd7a:115c:a1e0::1"]
	  },
	  "Peer": {}
	}`)
	self, err := ParseLocalTailscaleStatus(live)
	if err != nil {
		t.Fatalf("parse the live shape: %v", err)
	}
	if self.HostName != "exit-node-vps" {
		t.Errorf("HostName = %q, want exit-node-vps", self.HostName)
	}
	if self.BackendState != "Running" {
		t.Errorf("BackendState = %q, want Running", self.BackendState)
	}
	if len(self.IPs) != 2 || self.IPs[0] != "100.64.0.1" || self.IPs[1] != "fd7a:115c:a1e0::1" {
		t.Errorf("IPs = %v, want both families", self.IPs)
	}

	// A daemon that has never logged in: no HostName, only DNSName, no addresses.
	//
	// Sanitized sample: the real hostnames/IPs of this deployment live in
	// docs/operations.md, not in unit-test fixtures.
	empty, err := ParseLocalTailscaleStatus([]byte(`{"Self":{"DNSName":"fallback-name.tailnet.example.ts.net."}}`))
	if err != nil {
		t.Fatalf("parse the logged-out shape: %v", err)
	}
	if empty.HostName != "fallback-name.tailnet.example.ts.net" {
		t.Errorf("HostName = %q, want the DNSName fallback without the trailing dot", empty.HostName)
	}
	if len(empty.IPs) != 0 {
		t.Errorf("IPs = %v, want none", empty.IPs)
	}

	if _, err := ParseLocalTailscaleStatus([]byte("not json")); err == nil {
		t.Error("a non-JSON body must be an error, not an empty identity")
	}
}

func TestIsLocalRelay_B293(t *testing.T) {
	self := LocalSelf{HostName: "exit-node-vps", IPs: []string{"100.64.0.1", "fd7a:115c:a1e0::1"}}

	cases := []struct {
		name    string
		nodeIPs []string
		want    bool
		wantIP  string
	}{
		{"the operator's relley (IPv4 first)", []string{"100.64.0.1", "fd7a:115c:a1e0::1"}, true, "100.64.0.1"},
		{"match on the IPv6 entry only", []string{"fd7a:115c:a1e0::1"}, true, "fd7a:115c:a1e0::1"},
		{"match with surrounding whitespace", []string{"  100.64.0.1 "}, true, "100.64.0.1"},
		{"a remote relay", []string{"100.64.0.7"}, false, ""},
		{"no addresses at all", nil, false, ""},
		{"empty strings are not a match", []string{"", "  "}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, got := IsLocalRelay(self, c.nodeIPs)
			if got != c.want {
				t.Fatalf("IsLocalRelay(%v) = %v, want %v", c.nodeIPs, got, c.want)
			}
			if ip != c.wantIP {
				t.Errorf("matched address = %q, want %q", ip, c.wantIP)
			}
		})
	}

	// A HOSTNAME must never be enough: the B265.1 lesson (`skygate-host` matched
	// every relay) is exactly why the transport decision uses addresses only.
	other := LocalSelf{HostName: "exit-node-vps", IPs: []string{"100.64.0.9"}}
	if _, got := IsLocalRelay(other, []string{"100.64.0.42"}); got {
		t.Error("a shared hostname must not make a remote relay look local — only an owned address may")
	}
}

func TestSelfCoveringRoutes_B293(t *testing.T) {
	// The host is 100.64.0.1 and also lives in 172.17.0.0/16 (docker) and
	// 192.0.2.0/24 (its LAN).
	selfIPs := []string{"100.64.0.1", "172.17.0.2", "192.0.2.10"}

	routes := []string{
		"0.0.0.0/0",     // exit-node base — MUST survive
		"::/0",          // exit-node base — MUST survive
		"172.17.0.0/16", // the host itself is inside it → the documented loop
		"192.0.2.0/24",  // same
		"10.0.0.0/8",    // unrelated subnet — keep
		"2001:db8::/32", // unrelated v6 — keep
		"not-a-cidr",    // unparseable: leave it to tailscale rather than guess
		"  ",            // blank: dropped
	}
	kept, skipped := SelfCoveringRoutes(routes, selfIPs)

	wantSkipped := map[string]bool{"172.17.0.0/16": true, "192.0.2.0/24": true}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want exactly the two self-covering subnets", skipped)
	}
	for _, s := range skipped {
		if !wantSkipped[s] {
			t.Errorf("unexpectedly skipped %q", s)
		}
	}
	for _, want := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8", "2001:db8::/32", "not-a-cidr"} {
		found := false
		for _, k := range kept {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %q must be KEPT (kept=%v)", want, kept)
		}
	}

	// The base routes are exempt even though they contain every address — they are
	// what makes the node an exit node, and tailscale never installs an exit route
	// into the advertising node itself.
	for _, k := range kept {
		if k == "0.0.0.0/0" || k == "::/0" {
			continue
		}
		if strings.Contains(k, "172.17") || strings.Contains(k, "192.0.2") {
			t.Errorf("self-covering route %q leaked into the kept list", k)
		}
	}

	// No self addresses (a remote relay, or a daemon we could not identify): the
	// filter must be a pass-through.
	keptAll, skippedNone := SelfCoveringRoutes([]string{"0.0.0.0/0", "::/0", "10.0.0.0/8"}, nil)
	if len(keptAll) != 3 || len(skippedNone) != 0 {
		t.Errorf("with no self addresses: kept=%v skipped=%v, want everything kept", keptAll, skippedNone)
	}
}

// TestDetectRelayPlacement_B293_1 — the fallback that made the live host work.
//
// The native install runs skygate as an unprivileged service user and
// tailscaled's socket is root-owned unless `--operator` was granted, so
// `tailscale status --json` fails there. Detection must then fall back to the
// addresses bound on THIS machine's interfaces (readable by anyone) instead of
// silently staying on SSH — the operator's «команда ничего не дала».
func TestDetectRelayPlacement_B293_1(t *testing.T) {
	origSelf, origIfaces := localSelfFn, localIfacesFn
	t.Cleanup(func() { localSelfFn, localIfacesFn = origSelf, origIfaces })

	// 1. The daemon answers and owns the relay address → best evidence.
	localSelfFn = func() (LocalSelf, error) {
		return LocalSelf{HostName: "exit-node-vps", IPs: []string{"100.64.0.1"}}, nil
	}
	localIfacesFn = func() ([]string, error) { return []string{"192.0.2.10"}, nil }
	p := DetectRelayPlacement([]string{"100.64.0.1"})
	if !p.Local || p.Evidence != "local tailscaled" || p.MatchedIP != "100.64.0.1" {
		t.Errorf("daemon evidence: %+v, want local via local tailscaled", p)
	}

	// 2. The daemon cannot be read (permission denied) but the address IS bound
	//    locally → still the local transport, with the daemon error carried along
	//    for the log.
	localSelfFn = func() (LocalSelf, error) {
		return LocalSelf{}, errors.New("permission denied opening /var/run/tailscale/tailscaled.sock")
	}
	localIfacesFn = func() ([]string, error) { return []string{"192.0.2.10", "100.64.0.1"}, nil }
	p = DetectRelayPlacement([]string{"100.64.0.1", "fd7a:115c:a1e0::1"})
	if !p.Local || p.Evidence != "local interface" {
		t.Fatalf("interface evidence: %+v, want local via local interface", p)
	}
	if p.DaemonErr == nil {
		t.Error("the daemon error must be carried for the log — a silent fallback hides a root-owned socket")
	}
	if len(p.SelfIPs) == 0 {
		t.Error("SelfIPs must be filled so the self-covering-route guard works")
	}

	// 3. A NEGATIVE answer from the daemon is final: the interface list must not
	//    turn a remote relay into a local one.
	localSelfFn = func() (LocalSelf, error) { return LocalSelf{IPs: []string{"100.64.0.9"}}, nil }
	localIfacesFn = func() ([]string, error) { return []string{"100.64.0.42"}, nil }
	if p = DetectRelayPlacement([]string{"100.64.0.7"}); p.Local {
		t.Errorf("a remote relay must stay remote: %+v", p)
	}

	// 4. Neither source answers → not local, no panic, and the daemon error is
	//    reported so the caller can log why SSH was used.
	localSelfFn = func() (LocalSelf, error) { return LocalSelf{}, errors.New("tailscale: not found") }
	localIfacesFn = func() ([]string, error) { return nil, errors.New("no interfaces") }
	p = DetectRelayPlacement([]string{"100.64.0.1"})
	if p.Local || p.DaemonErr == nil {
		t.Errorf("no evidence: %+v, want not-local with the daemon error", p)
	}
}

// TestLocalInterfaceIPs_B293_1 — the privilege-free probe returns real addresses
// and never the loopback (a loopback match would make every relay look local).
func TestLocalInterfaceIPs_B293_1(t *testing.T) {
	ips, err := LocalInterfaceIPs()
	if err != nil {
		t.Skipf("no interface list on this host: %v", err)
	}
	for _, ip := range ips {
		if ip == "127.0.0.1" || ip == "::1" {
			t.Errorf("LocalInterfaceIPs returned the loopback (%v)", ips)
		}
		if ip == "" {
			t.Errorf("LocalInterfaceIPs returned an empty entry: %v", ips)
		}
	}
}

// TestRoutesFallbackHint_B293 pins the four documented ways out of a privilege
// refusal — the operator asked for this explicitly ("бывает что ставят без
// [root] … чтобы не было ситуации что нет возможности настроить").
func TestRoutesFallbackHint_B293(t *testing.T) {
	hint := RoutesFallbackHint()
	for _, want := range []string{"root", "--operator", "sudoers", "install-routes-helper.sh"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint = %q, want it to name %q", hint, want)
		}
	}
}
