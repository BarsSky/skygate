// relaykind_b401_test.go — 2026-10-20 (B401): one answer to "what IS this row?".
//
// THE REPORT (operator, verbatim): «в релеи висят еще два не рабочих derp что это и
// к чему». His `derp_relays` table held exactly three rows and only one is a relay:
//
//	https://derp.skynas.ru:443                          the relay (healthy, 5 ms)
//	https://derp.skynas.ru:8443                         the LAN-direct HTTP port
//	https://controlplane.tailscale.com/derpmap/default  a DERP MAP DOCUMENT
//
// The classifier is the single place that decides this, so the page, the map publisher
// and this test cannot disagree again.
package derphealth

import "testing"

func TestB401_ClassifyRelayURL(t *testing.T) {
	cases := []struct {
		url  string
		kind RelayKind
	}{
		// The real relay, in every shape the protocol serves.
		{"https://derp.skynas.ru", RelayKindNode},
		{"https://derp.skynas.ru:443", RelayKindNode},
		{"https://derp.skynas.ru/", RelayKindNode},
		{"https://derp.skynas.ru/derp", RelayKindNode},
		{"https://derp.skynas.ru:8443", RelayKindNode}, // a port does NOT make it a non-relay
		{"http://relay.example.com/derp/probe", RelayKindNode},
		// The live junk row: a catalogue, not a node.
		{"https://controlplane.tailscale.com/derpmap/default", RelayKindDerpMap},
		{"https://example.com/derpmap", RelayKindDerpMap},
		// A path the DERP protocol does not serve.
		{"https://example.com/admin", RelayKindOtherPath},
		// Unusable input is reported, never guessed into a node.
		{"", RelayKindOtherPath},
		{"not a url", RelayKindOtherPath},
		{"https://", RelayKindOtherPath},
	}
	for _, c := range cases {
		got, why := ClassifyRelayURL(c.url)
		if got != c.kind {
			t.Errorf("ClassifyRelayURL(%q) = %q, want %q", c.url, got, c.kind)
		}
		if c.kind != RelayKindNode && why == "" {
			t.Errorf("ClassifyRelayURL(%q) gave no reason — a non-node row must be explainable", c.url)
		}
		if c.kind == RelayKindNode && why != "" {
			t.Errorf("ClassifyRelayURL(%q) is a node but carries a reason %q", c.url, why)
		}
	}
}

func TestB401_RelayPortAndHost(t *testing.T) {
	cases := []struct {
		url  string
		port int
		host string
	}{
		{"https://derp.skynas.ru", 443, "derp.skynas.ru"},
		{"https://derp.skynas.ru:8443", 8443, "derp.skynas.ru"},
		{"https://DERP.Skynas.ru:443/derp", 443, "derp.skynas.ru"},
		{"", 443, ""},
	}
	for _, c := range cases {
		if got := RelayPort(c.url); got != c.port {
			t.Errorf("RelayPort(%q) = %d, want %d", c.url, got, c.port)
		}
		if got := RelayHost(c.url); got != c.host {
			t.Errorf("RelayHost(%q) = %q, want %q", c.url, got, c.host)
		}
	}
}

// TestB401_AltPortIsTheSameHostOnAnotherPort is the `:8443` shape: two rows for one
// relay. It must be DETECTED (so the page can label it) without being called broken —
// the LAN port is legitimate, it is just not a second relay.
func TestB401_AltPortIsTheSameHostOnAnotherPort(t *testing.T) {
	others := []string{"https://derp.skynas.ru:443"}
	alt := ClassifyAltPort("https://derp.skynas.ru:8443", others)
	if !alt.IsAltPort {
		t.Fatal("the :8443 row was not recognised as the same host on another port")
	}
	if alt.SameHost != "derp.skynas.ru" || alt.OtherPort != 443 {
		t.Errorf("alt = %+v, want host derp.skynas.ru on the other row's port 443", alt)
	}
	// The conventional row itself is not an "alternative" of anything.
	if alt := ClassifyAltPort("https://derp.skynas.ru:443", others); alt.IsAltPort {
		t.Error("the :443 row was reported as an alternative port of itself")
	}
	// A different host on a different port is a different relay, not an alternative.
	if alt := ClassifyAltPort("https://other.example.com:8443", others); alt.IsAltPort {
		t.Error("a different host was reported as an alternative port of derp.skynas.ru")
	}
	// No other rows at all → nothing to compare.
	if alt := ClassifyAltPort("https://derp.skynas.ru:8443", nil); alt.IsAltPort {
		t.Error("an empty row set produced an alternative-port verdict")
	}
}

// TestB401_RealRelayIsStillARelay pins the one row that must never be flagged: the
// operator's own relay would otherwise be "cleaned up" by a later block.
func TestB401_RealRelayIsStillARelay(t *testing.T) {
	kind, why := ClassifyRelayURL("https://derp.skynas.ru:443")
	if kind != RelayKindNode || why != "" {
		t.Fatalf("the operator's own relay classified as (%q, %q)", kind, why)
	}
	if alt := ClassifyAltPort("https://derp.skynas.ru:443", []string{"https://derp.skynas.ru:8443"}); alt.IsAltPort && alt.OtherPort == 443 {
		t.Error("the conventional row was labelled an alternative of the LAN port")
	}
}
