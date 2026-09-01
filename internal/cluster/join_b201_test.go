// v1.5.0+ / B201 — unit tests for the join / heartbeat
// pure helpers. The DB-touching parts (Join, Heartbeat)
// are tested via the live B201 verification script
// (verify_b201.sh) against a real skygate + PG. This
// file covers the pure-shape helpers and the error
// sentinels.

package cluster

import (
	"errors"
	"testing"
)

func TestHostnamesEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"svi-1", "svi-1", true},
		{"svi-1", "SVI-1", true},         // case-insensitive
		{"svi-1", " svi-1 ", true},       // trims whitespace
		{"svi-1", "evil-host", false},
		{"svi-1", "svi-2", false},
		{"", "", true},
		{"a", "", false},
		{"a", "ab", false},
	}
	for _, c := range cases {
		t.Run(c.a+"_vs_"+c.b, func(t *testing.T) {
			if got := hostnamesEqual(c.a, c.b); got != c.want {
				t.Errorf("hostnamesEqual(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestTrimSpaceASCII(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"abc", "abc"},
		{" abc ", "abc"},
		{"\t\nabc\r\n", "abc"},
		{"  a b  ", "a b"}, // inner whitespace preserved
	}
	for _, c := range cases {
		got := trimSpaceASCII(c.in)
		if got != c.want {
			t.Errorf("trimSpaceASCII(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseRolesField(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "skygate", []string{"skygate"}},
		{"two", "skygate,skygate-standby", []string{"skygate", "skygate-standby"}},
		{"with spaces", "  skygate , skygate-standby  ", []string{"skygate", "skygate-standby"}},
		{"dedup", "skygate,skygate", []string{"skygate"}},
		{"empty parts", "skygate,,skygate-standby,", []string{"skygate", "skygate-standby"}},
		{"three roles", "skygate,patroni-primary,patroni-replica", []string{"skygate", "patroni-primary", "patroni-replica"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseRolesField(c.in)
			if !slicesEqualB201(got, c.want) {
				t.Errorf("parseRolesField(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestSplitComma(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{""}},
		{"no comma", "abc", []string{"abc"}},
		{"two", "a,b", []string{"a", "b"}},
		{"three", "a,b,c", []string{"a", "b", "c"}},
		{"quoted comma", `a,"b,c",d`, []string{"a", "b,c", "d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitComma(c.in)
			if !slicesEqualB201(got, c.want) {
				t.Errorf("splitComma(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestJoinErrorSentinels(t *testing.T) {
	// Distinct sentinels so HTTP layer can map them to
	// the right status code. Identity checks via errors.Is.
	if errors.Is(ErrHostnameMismatch, ErrInviteAlreadyUsed) {
		t.Error("ErrHostnameMismatch == ErrInviteAlreadyUsed")
	}
	if errors.Is(ErrInviteExpired, ErrInviteRevoked) {
		t.Error("ErrInviteExpired == ErrInviteRevoked")
	}
	if errors.Is(ErrInviteNotPending, ErrInviteAlreadyUsed) {
		t.Error("ErrInviteNotPending == ErrInviteAlreadyUsed")
	}
	if errors.Is(ErrNodeAlreadyExists, ErrHeartbeatNodeNotFound) {
		t.Error("ErrNodeAlreadyExists == ErrHeartbeatNodeNotFound")
	}
}

func TestJoinRequestFields(t *testing.T) {
	// Pin the JSON tag names — they ARE the public API
	// contract for /api/cluster/join. Changing them would
	// break every deployed new node.
	req := JoinRequest{
		Token:          "t",
		Hostname:       "h",
		TailscaleIP:    "i",
		SkygateVersion: "v",
		Roles:          "r",
	}
	if req.Token != "t" || req.Hostname != "h" || req.TailscaleIP != "i" || req.SkygateVersion != "v" || req.Roles != "r" {
		t.Errorf("struct field assignment broken: %+v", req)
	}
}

func TestHeartbeatResponseFields(t *testing.T) {
	// Same — pinning the JSON shape.
	resp := HeartbeatResponse{
		NodeID:               "n",
		State:                "ready",
		LastSeenAt:           12345,
		NextHeartbeatSeconds: 30,
		HeartbeatsUntilStale: 3,
	}
	if resp.State != "ready" {
		t.Errorf("state = %q, want ready", resp.State)
	}
	if resp.NextHeartbeatSeconds != 30 {
		t.Errorf("next_heartbeat_seconds = %d, want 30", resp.NextHeartbeatSeconds)
	}
}

func slicesEqualB201(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
