package admin

// derp_mapaddr_b510_test.go — B510 (2026-11-25).
//
// The bug these tests pin is an ABSENCE: our own relay's map entry had no IPv4 and no IPv6,
// and nothing anywhere said why that matters. Cellular clients are IPv6-first, so the
// absence cost them a stable relay choice; Wi-Fi clients take a direct path and never
// noticed. What must stay true: the hints are what the NAME actually resolves to (a guess
// would be worse than silence), an unreachable IPv6 form is never published, and the
// missing-AAAA case is REPORTED rather than looping silently in netcheck.

import (
	"encoding/json"
	"net"
	"testing"
)

func TestB510_PicksThePublicAddresses(t *testing.T) {
	cases := []struct {
		name   string
		ips    []net.IP
		wantV4 string
		wantV6 string
	}{
		{
			name:   "v4 and global v6",
			ips:    []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("2001:db8::10")},
			wantV4: "203.0.113.10",
			wantV6: "2001:db8::10",
		},
		{
			name:   "v4 only",
			ips:    []net.IP{net.ParseIP("203.0.113.10")},
			wantV4: "203.0.113.10",
			wantV6: "",
		},
		{
			// A ULA is not reachable from the internet; publishing it as a DERP hint would
			// make every v6-capable client dial an address that cannot answer.
			name:   "ULA v6 is not a hint",
			ips:    []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("fd7a:115c:a1e0::14")},
			wantV4: "203.0.113.10",
			wantV6: "",
		},
		{
			name:   "link-local v6 is not a hint",
			ips:    []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("fe80::1")},
			wantV4: "203.0.113.10",
			wantV6: "",
		},
		{
			name:   "loopback is not a hint",
			ips:    []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
			wantV4: "",
			wantV6: "",
		},
		{
			name:   "nil entries are tolerated",
			ips:    []net.IP{nil, net.ParseIP("203.0.113.10")},
			wantV4: "203.0.113.10",
			wantV6: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v4, v6 := pickDerpAddresses(c.ips)
			if v4 != c.wantV4 || v6 != c.wantV6 {
				t.Errorf("pickDerpAddresses = (%q, %q), want (%q, %q)", v4, v6, c.wantV4, c.wantV6)
			}
		})
	}
}

func TestB510_EmptyHostNeedsNoLookup(t *testing.T) {
	if v4, v6 := derpNodeAddresses(""); v4 != "" || v6 != "" {
		t.Errorf("derpNodeAddresses(\"\") = (%q, %q), want empty", v4, v6)
	}
	if v4, v6 := derpNodeAddresses("   "); v4 != "" || v6 != "" {
		t.Errorf("derpNodeAddresses(blank) = (%q, %q), want empty", v4, v6)
	}
}

func TestB510_MissingV6IsNamedNotSilent(t *testing.T) {
	host := "relay.example.com"
	warn := noteDerpAddressGap(host, "203.0.113.10", "")
	if warn == "" {
		t.Fatal("an IPv4-only relay produced no warning — this is exactly the silence that cost the operator a week of spikes")
	}
	for _, want := range []string{host, "AAAA", "464XLAT", "IPv6"} {
		if !contains(warn, want) {
			t.Errorf("the warning must name %q, got: %s", want, warn)
		}
	}
	// Rate-limited: the map is regenerated on every fetch, so the same host must not
	// produce a line per fetch.
	if again := noteDerpAddressGap(host, "203.0.113.10", ""); again != "" {
		t.Errorf("the warning is not rate-limited (second call still returned a line): %s", again)
	}
}

func TestB510_NoWarningWhenV6Exists(t *testing.T) {
	if warn := noteDerpAddressGap("relay-with-v6.example.com", "203.0.113.10", "2001:db8::10"); warn != "" {
		t.Errorf("a relay with IPv6 must not be warned about: %s", warn)
	}
}

// TestB510_MapJSONCarriesTheHints is the contract the CLIENT sees: Tailscale's derpmap
// fields are named IPv4/IPv6, and an absent address must be OMITTED (not an empty string,
// which a parser would treat as a hint of "").
func TestB510_MapJSONCarriesTheHints(t *testing.T) {
	withBoth := derpMapNode{
		Name: "mow-1", RegionID: 900, HostName: "relay.example.com",
		DERPPort: 8443, STUNPort: 3478,
		IPv4: "203.0.113.10", IPv6: "2001:db8::10",
	}
	b, err := json.Marshal(withBoth)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["IPv4"] != "203.0.113.10" || back["IPv6"] != "2001:db8::10" {
		t.Errorf("the map node must carry IPv4/IPv6 hints, got: %s", string(b))
	}

	noV6 := withBoth
	noV6.IPv6 = ""
	b2, err := json.Marshal(noV6)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back2 map[string]any
	if err := json.Unmarshal(b2, &back2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := back2["IPv6"]; present {
		t.Errorf("an absent IPv6 must be OMITTED, not published as an empty hint: %s", string(b2))
	}
	if back2["IPv4"] != "203.0.113.10" {
		t.Errorf("IPv4 must survive the omission of IPv6: %s", string(b2))
	}
}
