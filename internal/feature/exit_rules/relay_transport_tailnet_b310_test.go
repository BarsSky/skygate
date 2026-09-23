// internal/feature/exit_rules/relay_transport_tailnet_b310_test.go — B310 (v1.5.75).
//
// The live case these tests pin: the operator's relay row pointed at the TAILNET
// address 100.64.0.2:18022 (the right idea — that path survives a blocked public
// IP) while the skygate host had no tailscaled at all, so `ip route get 100.64.0.2`
// answered "via 192.168.13.1 dev ens18" and every sync ended in an unexplained
// "Operation timed out". The ladder must (a) prefer the tailnet path when it can
// work, (b) keep the operator's target as the fallback, and (c) SAY that the tailnet
// path cannot work instead of timing out silently.
package exit_rules

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsTailnetAddress_B310(t *testing.T) {
	cases := map[string]bool{
		"100.64.0.2":             true,
		"100.64.0.1":             true,
		"100.127.255.255":        true,
		"100.63.255.255":         false,
		"100.128.0.1":            false,
		"8.8.8.8":                false,
		"fd7a:115c:a1e0::2":      true,
		"fd7a:115c:a1e0:ab12::1": true,
		"fd00::1":                false,
		"relay.example.com":      false,
		"":                       false,
	}
	for in, want := range cases {
		if got := IsTailnetAddress(in); got != want {
			t.Errorf("IsTailnetAddress(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestSplitSSHEndpoint_B310(t *testing.T) {
	cases := []struct {
		in               string
		user, host, port string
	}{
		{"root@100.64.0.2:18022", "root", "100.64.0.2", "18022"},
		{"root@relay.example.com", "root", "relay.example.com", ""},
		{"relay.example.com:2222", "", "relay.example.com", "2222"},
		{"root@[fd7a:115c:a1e0::2]:18022", "root", "fd7a:115c:a1e0::2", "18022"},
		{"fd7a:115c:a1e0::2", "", "fd7a:115c:a1e0::2", ""},
		{"relay-4", "", "relay-4", ""},
	}
	for _, c := range cases {
		u, h, p := splitSSHEndpoint(c.in)
		if u != c.user || h != c.host || p != c.port {
			t.Errorf("splitSSHEndpoint(%q) = (%q,%q,%q); want (%q,%q,%q)", c.in, u, h, p, c.user, c.host, c.port)
		}
	}
}

func TestRelayEndpointTarget_B310(t *testing.T) {
	cases := []struct {
		ep   RelayEndpoint
		want string
	}{
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}, "root@100.64.0.2:18022"},
		{RelayEndpoint{Kind: RelayEndpointPublic, User: "deploy", Host: "relay.example.com"}, "deploy@relay.example.com"},
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.3", Port: "22"}, "root@100.64.0.3"},
	}
	for _, c := range cases {
		if got := c.ep.Target(); got != c.want {
			t.Errorf("Target() = %q; want %q", got, c.want)
		}
	}
	if got := (RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}).Label(); got != "tailnet 100.64.0.2:18022" {
		t.Errorf("Label() = %q", got)
	}
}

func TestProbeRelayEndpoint_B310(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if err := ProbeRelayEndpoint(RelayEndpoint{Kind: RelayEndpointPublic, Host: "127.0.0.1", Port: port}, time.Second); err != nil {
		t.Errorf("a listening port must probe OK, got %v", err)
	}
	// A port nobody listens on must report the transport failure, not a hang.
	if err := ProbeRelayEndpoint(RelayEndpoint{Kind: RelayEndpointTailnet, Host: "127.0.0.1", Port: "9"}, time.Second); err == nil {
		t.Errorf("a closed port must fail the probe")
	}
}

// TestRelaySSHEndpoints_PrefersTailnetWhenSkygateIsOnIt.
func TestRelaySSHEndpoints_PrefersTailnetWhenSkygateIsOnIt(t *testing.T) {
	pinTailnet(t, TailnetState{Ready: true, IP: "100.64.0.9", Iface: "tailscale0", Reason: "test"})
	cfg := relaySSHConfig{SSHTarget: "root@relay.example.com:18022", SSHPort: "18022"}
	cands, notes := relaySSHEndpoints(nil, cfg, "relay-4")
	if len(notes) != 0 {
		t.Fatalf("no notes expected when skygate is on the tailnet, got %v", notes)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates (public + name), got %d: %+v", len(cands), cands)
	}
	if cands[0].Kind != RelayEndpointPublic || cands[0].Host != "relay.example.com" || cands[0].Port != "18022" {
		t.Errorf("first candidate should be the operator target, got %+v", cands[0])
	}
	if cands[1].Kind != RelayEndpointName || cands[1].Host != "relay-4" {
		t.Errorf("last candidate should be the node name, got %+v", cands[1])
	}
}

// TestRelaySSHEndpoints_TailnetTargetFirstWhenSkygateCanUseIt: the operator's
// ssh_target IS a tailnet address (the live karolina row), so with skygate on the
// tailnet that address becomes the preferred candidate.
func TestRelaySSHEndpoints_TailnetTargetFirstWhenSkygateCanUseIt(t *testing.T) {
	pinTailnet(t, TailnetState{Ready: true, IP: "100.64.0.9", Reason: "test"})
	cfg := relaySSHConfig{SSHTarget: "root@100.64.0.2:18022"}
	cands, notes := relaySSHEndpoints(nil, cfg, "karolina")
	if len(notes) != 0 {
		t.Fatalf("no notes expected, got %v", notes)
	}
	if len(cands) == 0 || cands[0].Kind != RelayEndpointTailnet || cands[0].Host != "100.64.0.2" || cands[0].Port != "18022" {
		t.Fatalf("the tailnet target must be the first candidate, got %+v", cands)
	}
	if len(cands) != 2 || cands[1].Kind != RelayEndpointName {
		t.Fatalf("candidates should be [tailnet, name], got %+v", cands)
	}
}

// TestRelaySSHEndpoints_NotOnTheTailnetNamesTheReason is the live regression: the
// row points at a tailnet address, skygate is not on the tailnet, and the failure
// must SAY that instead of timing out.
func TestRelaySSHEndpoints_NotOnTheTailnetNamesTheReason(t *testing.T) {
	pinTailnet(t, TailnetState{Ready: false, Reason: "tailscaled is not running: dial unix /var/run/tailscale/tailscaled.sock: connect: no such file or directory"})
	cfg := relaySSHConfig{SSHTarget: "root@100.64.0.2:18022"}
	cands, notes := relaySSHEndpoints(nil, cfg, "karolina")
	if len(notes) == 0 {
		t.Fatalf("the missing tailnet path must be named in the notes")
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "skygate itself is not on the tailnet") {
		t.Errorf("note must say skygate is not on the tailnet, got %q", joined)
	}
	if !strings.Contains(joined, "100.64.0.2") {
		t.Errorf("note must name the address that cannot work, got %q", joined)
	}
	// The tailnet candidate stays in the list (a host may reach 100.64/10 through a
	// subnet router) but only AFTER the public ones — here there is no public one,
	// so it is last before the node name.
	if len(cands) != 2 || cands[1].Kind != RelayEndpointName {
		t.Fatalf("want [tailnet, name] with the tailnet candidate tried first, got %+v", cands)
	}
}

// TestRelaySSHEndpoints_PublicFirstWhenSkygateIsOffTheTailnet.
func TestRelaySSHEndpoints_PublicFirstWhenSkygateIsOffTheTailnet(t *testing.T) {
	pinTailnet(t, TailnetState{Ready: false, Reason: "no tailnet interface"})
	cfg := relaySSHConfig{SSHTarget: "deploy@relay.example.com:2200", SSHPort: "2200"}
	cands, _ := relaySSHEndpoints(nil, cfg, "relay-4")
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %+v", cands)
	}
	if cands[0].Kind != RelayEndpointPublic || cands[0].User != "deploy" || cands[0].Port != "2200" {
		t.Errorf("the public target must be tried first when the tailnet is unusable, got %+v", cands[0])
	}
}

// TestApplyRoutesOverSSHLadder_NoCandidatesNamesIt.
func TestApplyRoutesOverSSHLadder_NoCandidatesNamesIt(t *testing.T) {
	res := applyRoutesOverSSHLadder(nil, "relay-4", []string{"0.0.0.0/0"}, 1, "/tmp/key", nil, nil)
	if res.OK {
		t.Fatalf("an empty candidate list cannot succeed")
	}
	if len(res.Attempts) == 0 || !strings.Contains(res.Attempts[0], "no usable transport") {
		t.Fatalf("the failure must name the missing transport, got %v", res.Attempts)
	}
}

// TestApplyRoutesOverSSHLadder_ReportsEveryCandidate: nothing answers, so the
// resulting error must name BOTH the dead candidates and the notes — the pre-B310
// message named one target, which is why the real cause stayed invisible.
func TestApplyRoutesOverSSHLadder_ReportsEveryCandidate(t *testing.T) {
	res := applyRoutesOverSSHLadder(nil, "karolina", []string{"0.0.0.0/0"}, 0, "/tmp/key",
		[]RelayEndpoint{
			{Kind: RelayEndpointTailnet, Host: "127.0.0.1", Port: "9"},
			{Kind: RelayEndpointName, Host: "127.0.0.1", Port: "9"},
		},
		[]string{"tailnet path unavailable (not on the tailnet): 100.64.0.2"},
	)
	if res.OK {
		t.Fatalf("nothing answers, so the ladder cannot succeed")
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("both candidates must be reported, got %v", res.Attempts)
	}
	if !strings.Contains(res.Attempts[0], "tailnet 127.0.0.1:9") || !strings.Contains(res.Attempts[1], "name 127.0.0.1:9") {
		t.Errorf("each attempt must name its transport and address, got %v", res.Attempts)
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "not on the tailnet") {
		t.Errorf("the notes must travel with the failure, got %v", res.Notes)
	}
}

// TestRecordRelayApplyStoresTheTransport_B310: the page has to be able to answer
// "which path is management using?" without reading the journal.
func TestRecordRelayApplyStoresTheTransport_B310(t *testing.T) {
	d := newB276DB(t)

	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=ok via tailnet", Via: "tailnet", Endpoint: "tailnet 100.64.0.2:18022"})
	st := RelayApplyStateOf(d, "karolina")
	if !st.OK || st.Via != "tailnet" || st.Endpoint != "tailnet 100.64.0.2:18022" {
		t.Fatalf("a successful apply must record the transport, got %+v", st)
	}

	recordRelayApply(d, "emilia", relayApplyOutcome{Label: "ssh=err=no reachable transport", Via: "public", Endpoint: "public relay.example.com:22"})
	st = RelayApplyStateOf(d, "emilia")
	if st.OK || st.Via != "public" || st.Endpoint != "public relay.example.com:22" {
		t.Fatalf("a failed apply must record where it tried, got %+v", st)
	}
	if !strings.Contains(st.Detail, "no reachable transport") {
		t.Errorf("the failure detail must survive, got %q", st.Detail)
	}

	// The list view (the page) must carry the same information.
	all := ListRelayApplyStates(d)
	if all["karolina"].Via != "tailnet" || all["emilia"].Via != "public" {
		t.Fatalf("ListRelayApplyStates must include the transport, got %+v", all)
	}

	// A relay that never applied has nothing recorded — the page must not invent
	// "unknown" rows for it.
	if st := RelayApplyStateOf(d, "never-seen"); st.Via != "" || st.At != 0 {
		t.Errorf("an unrecorded relay must read as empty, got %+v", st)
	}
}

// pinTailnet installs the test hook and restores it afterwards.
func pinTailnet(t *testing.T, st TailnetState) {
	t.Helper()
	prev := tailnetStateOverride
	tailnetStateOverride = &st
	resetTailnetStateCache()
	t.Cleanup(func() {
		tailnetStateOverride = prev
		resetTailnetStateCache()
	})
}
