// probe_b478_1_test.go — B478.1 (2026-11-15).
//
// «ПОЧЕМУ ТАКАЯ БОЛЬШАЯ ЗАДЕРЖКА ДЛЯ DERP НА ТОЙ ЖЕ МАШИНЕ?»
//
// Measured on the migration host, where derper and skygate share one box:
//
//	openssl s_client -connect 127.0.0.1:8443   → 0.01-0.04 s
//	openssl s_client -connect derp…:8443       → 0.05-0.21 s   (public IP, hairpin)
//	derp_health row for region 900            → latency 185-221 ms
//
// although `global_settings.derp.probe_host` was `127.0.0.1`. The pin was applied
// only when the relay's own name resolved to loopback (B296), and a relay that
// clients must reach announces a name that resolves to the PUBLIC address — so the
// operator's explicit instruction was silently ignored and the probe hairpinned out
// through the internet and back.
package derphealth

import (
	"testing"
)

// TestB478_1_ExplicitHintWinsOnAPubliclyResolvingName is the live shape: a relay
// name that resolves to a public address plus an explicit hint. The hint is what
// the operator asked for; it must be dialled.
func TestB478_1_ExplicitHintWinsOnAPubliclyResolvingName(t *testing.T) {
	d := DERPInfo{
		Host:      "relay.example.com", // resolves publicly in production
		URL:       "https://relay.example.com:8443",
		ProbeHost: "127.0.0.1",
	}
	addr, sni, port := dialTargetFor(d)
	if addr != "127.0.0.1" {
		t.Fatalf("addr = %q, want the explicit hint 127.0.0.1 — a pin that only applies to "+
			"loopback-resolving names is not a pin (B478.1)", addr)
	}
	if sni != "relay.example.com" {
		t.Errorf("sni = %q, want the relay's own name (the hint is an address, never the certificate name)", sni)
	}
	if port != "8443" {
		t.Errorf("port = %q, want 8443 from the row's URL", port)
	}
}

// TestB478_1_NoHintKeepsTheRowsOwnAddress: with nothing pinned, nothing changes —
// the probe dials what the row says. This is the guard against "fix the hint by
// always rewriting the address".
func TestB478_1_NoHintKeepsTheRowsOwnAddress(t *testing.T) {
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")
	d := DERPInfo{Host: "relay.example.com", URL: "https://relay.example.com:443"}
	addr, sni, port := dialTargetFor(d)
	if addr != "relay.example.com" || sni != "relay.example.com" || port != "443" {
		t.Fatalf("dialTargetFor = (%q,%q,%q), want the row's own address/name/port", addr, sni, port)
	}
}

// TestB478_1_RowHintBeatsTheEnvironment: the DB row is the operator-facing knob
// (the /admin/derp form writes it); an env default must not shadow it.
func TestB478_1_RowHintBeatsTheEnvironment(t *testing.T) {
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "192.0.2.70")
	d := DERPInfo{
		Host:      "relay.example.com",
		URL:       "https://relay.example.com:8443",
		ProbeHost: "127.0.0.1",
	}
	if addr, _, _ := dialTargetFor(d); addr != "127.0.0.1" {
		t.Fatalf("addr = %q, want the row's hint to win over the env default (B296 precedence)", addr)
	}
}
