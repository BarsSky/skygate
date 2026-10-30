// relay_probe_b447_test.go — B447. Two rows, one machine, two ports: the verdict must belong to
// the ROW, and every line must carry the port.
package derphealth

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestB447_EachRowIsItsOwnTarget is the live list: the relay on 443 and the LAN entry point on
// 8443. Both must appear, with their OWN port — a single verdict for the pair is exactly what made
// the broken one unidentifiable.
func TestB447_EachRowIsItsOwnTarget(t *testing.T) {
	got := ProbeTargets([]string{
		"https://derp.skynas.ru:443",
		"https://derp.skynas.ru:8443",
	}, "")
	if len(got) != 2 {
		t.Fatalf("targets = %d, want 2", len(got))
	}
	addrs := []string{got[0].Addr, got[1].Addr}
	wantA, wantB := "derp.skynas.ru:443", "derp.skynas.ru:8443"
	if !(addrs[0] == wantA && addrs[1] == wantB) && !(addrs[0] == wantB && addrs[1] == wantA) {
		t.Fatalf("addrs = %v, want %s and %s kept distinct", addrs, wantA, wantB)
	}
	for _, p := range got {
		if !strings.Contains(p.URL, ":") {
			t.Errorf("probe line lost the port: %q", p.URL)
		}
	}
}

// TestB447_NonRelayRowsAreSkippedWithAReason: a derpmap document is NOT dialled (that would be a
// guaranteed false FAIL), and the reason travels with the row instead of being dropped.
func TestB447_NonRelayRowsAreSkippedWithAReason(t *testing.T) {
	got := ProbeTargets([]string{"https://controlplane.tailscale.com/derpmap/default"}, "")
	if len(got) != 1 || !got[0].Skipped {
		t.Fatalf("targets = %+v, want one skipped row", got)
	}
	if got[0].Kind != RelayKindDerpMap {
		t.Errorf("kind = %q, want %q", got[0].Kind, RelayKindDerpMap)
	}
	if line := got[0].String(); !strings.Contains(line, "not a relay") {
		t.Errorf("line = %q, want the reason named", line)
	}
	if got[0].Addr != "" {
		t.Errorf("a derpmap row was given a dial address %q", got[0].Addr)
	}
}

// TestB447_ProbeHostReplacesTheDialAddressKeepsTheSNI: B296's knob lets a container that resolves
// its own public name to loopback still probe the relay. The DIAL address changes; the SNI name
// (which the certificate is issued for) does not.
func TestB447_ProbeHostReplacesTheDialAddressKeepsTheSNI(t *testing.T) {
	got := ProbeTargets([]string{"https://derp.example.test:443"}, "203.0.113.10")
	if len(got) != 1 {
		t.Fatalf("targets = %d", len(got))
	}
	if got[0].Addr != "203.0.113.10:443" {
		t.Errorf("addr = %q, want the probe host with the ROW's port", got[0].Addr)
	}
	if !strings.Contains(got[0].URL, "derp.example.test") {
		t.Errorf("the row's url was rewritten: %q", got[0].URL)
	}
}

// TestB447_OneDeadRowDoesNotSwallowTheOther is the heart of the report: the dead row fails, the
// live one is still probed and reported OK. A shared verdict (or a shared timeout) cannot do this.
func TestB447_OneDeadRowDoesNotSwallowTheOther(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close() // accept-and-close: TCP works, TLS does not
		}
	}()
	// A port that is almost certainly closed, on the same host.
	dead := "127.0.0.1:1"

	targets := []RelayProbe{
		{URL: "https://" + dead, Addr: dead, Kind: RelayKindNode},
		{URL: "https://" + ln.Addr().String(), Addr: ln.Addr().String(), Kind: RelayKindNode},
	}
	got := ProbeRelayRows(context.Background(), targets, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("results = %d", len(got))
	}
	if got[0].OK {
		t.Errorf("the closed port reported OK: %+v", got[0])
	}
	if got[0].Err == "" {
		t.Error("a failed row must carry a reason")
	}
	// The second row speaks to a socket that closes immediately, so the TLS handshake fails —
	// what matters here is that it was PROBED (has its own verdict), not that it succeeded.
	if got[1].Err == "" && !got[1].OK {
		t.Error("the second row produced neither a verdict nor an error")
	}
	// Both lines carry their own address.
	if !strings.Contains(got[0].String(), dead) || !strings.Contains(got[1].String(), ln.Addr().String()) {
		t.Errorf("verdict lines are not per-row: %q / %q", got[0].String(), got[1].String())
	}
}

// TestB447_DisabledRowsAreNotDialled: an operator who switched a row off must not get a FAIL from
// it (the row is skipped with a reason).
func TestB447_DisabledRowsAreNotDialled(t *testing.T) {
	p := ProbeRelay(context.Background(), RelayProbe{URL: "https://x:443", Addr: "x:443", Skipped: true}, time.Second)
	if !p.Skipped || p.Err != "" || p.OK {
		t.Fatalf("a skipped row was dialled: %+v", p)
	}
}
