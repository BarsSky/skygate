// v1.5.0+ / B223 — unit tests for the Tailscale
// auto-discovery helpers.
//
// The actual SQL execution + the `tailscale
// status --json` shell-out are exercised on the
// agent by the live-verify. These tests pin the
// pure-Go contracts:
//
//   1. TailscaleHostnameShort — the suffix-trim
//      logic (RFC-952-style tailnet DNS names).
//   2. firstIPv4 — the IPv4-only filter (cluster_node
//      is INET, v6 needs INET6).
//   3. matchesTagFilter — the "" = no filter rule
//      + the case-insensitive match.
//   4. parseTailscaleStatus — the JSON parser
//      happy path (Self + Peer map keyed by
//      DNS name).
//
// The shell-out (TailscaleStatusRaw) is mocked
// via the package-level tailscaleStatusFn hook —
// the same pattern as B222's selfHostnameFn. The
// tests assign + restore around each test that
// needs a fixed status output.

package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- TailscaleHostnameShort ---

func TestTailscaleHostnameShort_TailnetSuffix(t *testing.T) {
	// B223 contract: a full Tailscale DNS name
	// like "skygate-standby.tail.ts.net"
	// trims to the short form "skygate-standby"
	// (the B201 join flow's cluster_node.hostname
	// uses the short form).
	cases := []struct {
		in, want string
	}{
		{"skygate-standby.tail.ts.net", "skygate-standby"},
		{"laptop.tail.ts.net", "laptop"},
		// Single-label names (rare; happens
		// when the operator has a custom
		// MagicDNS suffix).
		{"skygate-primary", "skygate-primary"},
		// Two-label names that don't match
		// the tailnet pattern are returned
		// as-is.
		{"myhost.example.com", "myhost.example.com"},
	}
	for _, c := range cases {
		if got := TailscaleHostnameShort(c.in); got != c.want {
			t.Errorf("TailscaleHostnameShort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTailscaleHostnameShort_EmptyString(t *testing.T) {
	// B223 contract: empty input → empty
	// output (caller is expected to skip the
	// peer, but we don't panic).
	if got := TailscaleHostnameShort(""); got != "" {
		t.Errorf("TailscaleHostnameShort(\"\") = %q, want \"\"", got)
	}
}

// --- firstIPv4 ---

func TestFirstIPv4_MixedV4AndV6(t *testing.T) {
	// B223 contract: returns the first
	// IPv4 in the list (v6 entries are
	// skipped). Tailscale usually returns
	// [v4, v6] so this hits the common case.
	got := firstIPv4([]string{"100.64.0.5", "fd7a:115c:a1e0::5"})
	if got != "100.64.0.5" {
		t.Errorf("firstIPv4 = %q, want %q", got, "100.64.0.5")
	}
}

func TestFirstIPv4_V6Only(t *testing.T) {
	// B223 contract: a v6-only peer returns
	// empty (cluster_node.tailscale_ip is INET
	// which is v4-only; the B223 discover flow
	// will skip this peer because
	// TailscaleIP == "").
	got := firstIPv4([]string{"fd7a:115c:a1e0::5", "fd7a:115c:a1e0::6"})
	if got != "" {
		t.Errorf("firstIPv4 v6-only = %q, want \"\"", got)
	}
}

func TestFirstIPv4_Empty(t *testing.T) {
	// B223 contract: empty list → empty
	// string. Should not happen in practice
	// (Tailscale always assigns at least v6)
	// but defensive.
	if got := firstIPv4(nil); got != "" {
		t.Errorf("firstIPv4(nil) = %q, want \"\"", got)
	}
	if got := firstIPv4([]string{}); got != "" {
		t.Errorf("firstIPv4([]) = %q, want \"\"", got)
	}
}

// --- matchesTagFilter ---

func TestMatchesTagFilter_EmptyFilterMeansAll(t *testing.T) {
	// B223 contract: empty filter matches
	// every peer. This is the "no
	// SKYGATE_DISCOVERY_TAG set" case —
	// the operator accepts the risk of
	// every Tailscale device showing up.
	p := TailscalePeer{Hostname: "foo", Tags: nil}
	if !matchesTagFilter(p, "") {
		t.Error("matchesTagFilter(p, \"\") should be true (empty filter = all)")
	}
}

func TestMatchesTagFilter_CaseInsensitiveMatch(t *testing.T) {
	// B223 contract: Tailscale tags are
	// typically lowercase ("tag:skygate-
	// candidate") but the operator's env
	// might be uppercase. Case-insensitive
	// match keeps the surprise factor low.
	cases := []struct {
		peerTag   string
		filterTag string
		want      bool
	}{
		{"tag:skygate-candidate", "tag:skygate-candidate", true},
		{"tag:skygate-candidate", "tag:SKYGATE-CANDIDATE", true},
		{"tag:skygate-candidate", "tag:skygate-other", false},
		{"", "tag:skygate-candidate", false},
		{"tag:skygate-candidate", "", true}, // empty filter = all
	}
	for _, c := range cases {
		p := TailscalePeer{Tags: []string{c.peerTag}}
		if got := matchesTagFilter(p, c.filterTag); got != c.want {
			t.Errorf("matchesTagFilter(peer.Tag=%q, filter=%q) = %v, want %v", c.peerTag, c.filterTag, got, c.want)
		}
	}
}

// --- parseTailscaleStatus ---

func TestParseTailscaleStatus_HappyPath(t *testing.T) {
	// B223 contract: the parser handles the
	// Self + Peer map keyed by DNS name, with
	// the TailscaleIPs array + Tags array +
	// Online bool.
	raw := []byte(`{
		"BackendState": "Running",
		"Self": {
			"HostName": "agent.tail.ts.net",
			"TailscaleIPs": ["100.64.0.24", "fd7a:115c:a1e0::24"],
			"Online": true
		},
		"Peer": {
			"skygate-standby.tail.ts.net": {
				"HostName": "skygate-standby.tail.ts.net",
				"TailscaleIPs": ["100.64.0.5", "fd7a:115c:a1e0::5"],
				"Online": true,
				"Tags": ["tag:skygate-candidate"]
			},
			"laptop.tail.ts.net": {
				"HostName": "laptop.tail.ts.net",
				"TailscaleIPs": ["100.64.0.42"],
				"Online": false,
				"Tags": []
			}
		}
	}`)
	s, err := parseTailscaleStatus(raw)
	if err != nil {
		t.Fatalf("parseTailscaleStatus: %v", err)
	}
	if s.Self.Hostname != "agent" {
		t.Errorf("Self.Hostname = %q, want %q", s.Self.Hostname, "agent")
	}
	if s.Self.TailscaleIP != "100.64.0.24" {
		t.Errorf("Self.TailscaleIP = %q, want %q", s.Self.TailscaleIP, "100.64.0.24")
	}
	standby, ok := s.Peer["skygate-standby.tail.ts.net"]
	if !ok {
		t.Fatal("Peer[skygate-standby.tail.ts.net] missing")
	}
	if standby.Hostname != "skygate-standby" {
		t.Errorf("standby.Hostname = %q, want %q", standby.Hostname, "skygate-standby")
	}
	if !standby.Online {
		t.Error("standby.Online should be true")
	}
	if standby.TailscaleIP != "100.64.0.5" {
		t.Errorf("standby.TailscaleIP = %q, want %q", standby.TailscaleIP, "100.64.0.5")
	}
	if len(standby.Tags) != 1 || standby.Tags[0] != "tag:skygate-candidate" {
		t.Errorf("standby.Tags = %v, want [tag:skygate-candidate]", standby.Tags)
	}
	laptop, ok := s.Peer["laptop.tail.ts.net"]
	if !ok {
		t.Fatal("Peer[laptop.tail.ts.net] missing")
	}
	if laptop.Online {
		t.Error("laptop.Online should be false")
	}
}

func TestParseTailscaleStatus_InvalidJSON(t *testing.T) {
	// B223 contract: invalid JSON returns
	// an error (the caller logs + writes
	// cluster.discovery.error).
	_, err := parseTailscaleStatus([]byte("not json"))
	if err == nil {
		t.Error("parseTailscaleStatus(invalid) should return an error")
	}
	if !strings.Contains(err.Error(), "parse tailscale status") {
		t.Errorf("error message = %q, want prefix 'parse tailscale status'", err.Error())
	}
}

// --- TailscaleStatusRaw mock hook ---

func TestTailscaleStatusRaw_UsesMockFn(t *testing.T) {
	// B223 contract: when tailscaleStatusFn
	// is set, TailscaleStatusRaw returns
	// the mock's output without shelling
	// out. This is the pattern the unit
	// tests + the live-verify use to avoid
	// the `tailscale status` binary.
	orig := tailscaleStatusFn
	tailscaleStatusFn = func(ctx context.Context) ([]byte, error) {
		return []byte(`{"BackendState":"Running"}`), nil
	}
	defer func() { tailscaleStatusFn = orig }()
	got, err := TailscaleStatusRaw(context.Background())
	if err != nil {
		t.Fatalf("TailscaleStatusRaw: %v", err)
	}
	if !strings.Contains(string(got), "BackendState") {
		t.Errorf("TailscaleStatusRaw returned unexpected bytes: %q", string(got))
	}
}

func TestTailscaleStatusRaw_MockReturnsError(t *testing.T) {
	// B223 contract: when the mock returns
	// an error, TailscaleStatusRaw
	// propagates it. The caller logs +
	// writes cluster.discovery.error.
	mockErr := errors.New("simulated tailscaled down")
	orig := tailscaleStatusFn
	tailscaleStatusFn = func(ctx context.Context) ([]byte, error) {
		return nil, mockErr
	}
	defer func() { tailscaleStatusFn = orig }()
	_, err := TailscaleStatusRaw(context.Background())
	if err == nil {
		t.Error("TailscaleStatusRaw should propagate the mock error")
	}
	if !errors.Is(err, mockErr) {
		t.Errorf("error = %v, want wraps %v", err, mockErr)
	}
}
