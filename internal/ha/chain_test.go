// chain_test.go — unit tests for the HaChain type
// (Validate, Marshal/Unmarshal, SortedByPriority, IsAlive,
// FindByHostname, NextActiveToPromote, ApplyActiveRole).
//
// v1.5.0 (B145). All tests are pure (no DB, no HTTP) so
// they run under `go test ./internal/ha/` without any
// skygate test harness setup.

package ha

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHaChain_Validate_OK(t *testing.T) {
	c := &HaChain{
		Members: []HaMember{
			{Hostname: "skygate", Priority: 1, Role: RoleActive},
			{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestHaChain_Validate_Errors(t *testing.T) {
	tests := []struct {
		name    string
		chain   *HaChain
		wantSub string
	}{
		{
			name:    "nil chain",
			chain:   nil,
			wantSub: "nil chain",
		},
		{
			name:    "empty hostname",
			chain:   &HaChain{Members: []HaMember{{Hostname: "", Priority: 1}}},
			wantSub: "hostname is empty",
		},
		{
			name: "duplicate hostname",
			chain: &HaChain{Members: []HaMember{
				{Hostname: "skygate", Priority: 1},
				{Hostname: "skygate", Priority: 2},
			}},
			wantSub: "duplicate hostname",
		},
		{
			name:    "priority zero",
			chain:   &HaChain{Members: []HaMember{{Hostname: "skygate", Priority: 0}}},
			wantSub: "priority must be >= 1",
		},
		{
			name: "duplicate priority",
			chain: &HaChain{Members: []HaMember{
				{Hostname: "skygate", Priority: 1},
				{Hostname: "skygate-standby", Priority: 1},
			}},
			wantSub: "duplicate priority",
		},
		{
			name: "two actives",
			chain: &HaChain{Members: []HaMember{
				{Hostname: "skygate", Priority: 1, Role: RoleActive},
				{Hostname: "skygate-standby", Priority: 2, Role: RoleActive},
			}},
			wantSub: "at most one member may have role=active",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.chain.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSub, err.Error())
			}
		})
	}
}

func TestHaChain_SortedByPriority(t *testing.T) {
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate-standby", Priority: 2},
		{Hostname: "skygate", Priority: 1},
		{Hostname: "skygate-extra", Priority: 3},
	}}
	got := c.SortedByPriority()
	want := []string{"skygate", "skygate-standby", "skygate-extra"}
	for i, m := range got {
		if m.Hostname != want[i] {
			t.Errorf("SortedByPriority[%d] = %q, want %q", i, m.Hostname, want[i])
		}
	}
	// Verify the original slice was not mutated.
	if c.Members[0].Hostname != "skygate-standby" {
		t.Errorf("SortedByPriority mutated the receiver")
	}
}

func TestHaChain_FindByHostname(t *testing.T) {
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1},
		{Hostname: "skygate-standby", Priority: 2},
	}}
	if got := c.FindByHostname("skygate"); got != 0 {
		t.Errorf("FindByHostname(skygate) = %d, want 0", got)
	}
	if got := c.FindByHostname("skygate-standby"); got != 1 {
		t.Errorf("FindByHostname(skygate-standby) = %d, want 1", got)
	}
	if got := c.FindByHostname("missing"); got != -1 {
		t.Errorf("FindByHostname(missing) = %d, want -1", got)
	}
}

func TestHaMember_IsAlive(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name             string
		lastSeen         int64
		heartbeat        time.Duration
		threshold        int
		want             bool
	}{
		{"never seen", 0, 5 * time.Second, 3, false},
		{"just seen", now, 5 * time.Second, 3, true},
		{"seen 10s ago, threshold 15s", now - 10, 5 * time.Second, 3, true},
		{"seen 20s ago, threshold 15s", now - 20, 5 * time.Second, 3, false},
		{"seen 14s ago, threshold 15s", now - 14, 5 * time.Second, 3, true},
		{"seen 16s ago, threshold 15s", now - 16, 5 * time.Second, 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := HaMember{Hostname: "x", LastSeen: tc.lastSeen}
			if got := m.IsAlive(tc.heartbeat, tc.threshold); got != tc.want {
				t.Errorf("IsAlive(lastSeen=%d, hb=%s, thresh=%d) = %v, want %v",
					tc.lastSeen, tc.heartbeat, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestHaChain_ActiveMember(t *testing.T) {
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Role: RoleActive},
		{Hostname: "skygate-standby", Role: RoleStandby},
	}}
	if got := c.ActiveMember(); got == nil || got.Hostname != "skygate" {
		t.Errorf("ActiveMember = %v, want skygate", got)
	}
	c2 := &HaChain{Members: []HaMember{{Hostname: "x", Role: RoleStandby}}}
	if got := c2.ActiveMember(); got != nil {
		t.Errorf("ActiveMember = %v, want nil", got)
	}
}

func TestHaChain_NextActiveToPromote_SelfIsPrimary(t *testing.T) {
	now := time.Now().Unix()
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1, Role: RoleActive, LastSeen: now},
		{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby, LastSeen: now},
	}}
	// self (skygate-standby) is primary + alive → self wins.
	got := c.NextActiveToPromote(3, 5*time.Second, "skygate-standby", true)
	if got != "skygate-standby" {
		t.Errorf("NextActiveToPromote = %q, want skygate-standby", got)
	}
}

func TestHaChain_NextActiveToPromote_SelfNotPrimary(t *testing.T) {
	now := time.Now().Unix()
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1, Role: RoleActive, LastSeen: now},
		{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby, LastSeen: now},
	}}
	// self is NOT primary → lowest-priority alive wins.
	// Both alive; lowest priority is 2 (skygate-standby) — but
	// "self is not primary" → we DON'T self-promote, we
	// re-affirm the current active. Wait — re-read the
	// contract: lowest-priority alive. That's P2.
	// Actually the lowest priority NUMBER is the
	// HIGHEST-priority member (P1 = first choice). The
	// chain is sorted ASC by priority. "Lowest priority
	// alive" = first in sortedByPriority.
	// Hmm, this is ambiguous naming. Let me re-read the
	// contract: "lowest-priority alive member" — in HA
	// terminology, "lowest priority" usually means
	// "least preferred" (highest number). The chain
	// "promotes from the bottom" when the active dies.
	// Looking at SortedByPriority(): ascending by
	// priority (1, 2, 3). The first element is the
	// HIGHEST priority. So "lowest priority" = last
	// element of the sorted list = last-resort
	// promotion target.
	//
	// Wait that's backwards. Re-reading the spec:
	// "Priority (ascending: 1 is the highest priority /
	// preferred active)". So P1 is preferred. When the
	// active (P1) dies, promote P2 (next highest
	// priority = next-lowest priority NUMBER = second
	// in sorted list). So the implementation should
	// pick the FIRST element in SortedByPriority among
	// the alive members.
	//
	// Let me re-check: c.SortedByPriority returns P1,
	// P2, ... in that order. "First alive" = the
	// highest-priority alive member. That's the right
	// behavior.
	got := c.NextActiveToPromote(3, 5*time.Second, "skygate-standby", false)
	if got != "skygate" {
		t.Errorf("NextActiveToPromote = %q, want skygate (highest-priority alive)", got)
	}
}

func TestHaChain_NextActiveToPromote_ActiveUnreachable(t *testing.T) {
	now := time.Now().Unix()
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1, Role: RoleActive, LastSeen: now - 100}, // dead
		{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby, LastSeen: now}, // alive
	}}
	// P1 unreachable → P2 should be promoted.
	got := c.NextActiveToPromote(3, 5*time.Second, "skygate-standby", false)
	if got != "skygate-standby" {
		t.Errorf("NextActiveToPromote = %q, want skygate-standby", got)
	}
}

func TestHaChain_NextActiveToPromote_AllDead(t *testing.T) {
	now := time.Now().Unix()
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1, Role: RoleActive, LastSeen: now - 100},
		{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby, LastSeen: now - 100},
	}}
	// All dead → stay on the current active (don't
	// blank it out; operator intervention needed).
	got := c.NextActiveToPromote(3, 5*time.Second, "skygate-standby", false)
	if got != "skygate" {
		t.Errorf("NextActiveToPromote = %q, want skygate (current active)", got)
	}
}

func TestHaChain_ApplyActiveRole(t *testing.T) {
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Priority: 1, Role: RoleActive},
		{Hostname: "skygate-standby", Priority: 2, Role: RoleStandby},
	}}
	now := time.Now().Unix()
	if err := c.ApplyActiveRole("skygate-standby", now); err != nil {
		t.Fatalf("ApplyActiveRole: %v", err)
	}
	if c.Members[0].Role != RoleStandby {
		t.Errorf("P1 role = %q, want standby", c.Members[0].Role)
	}
	if c.Members[1].Role != RoleActive {
		t.Errorf("P2 role = %q, want active", c.Members[1].Role)
	}
	if c.Members[1].LastSeen != now {
		t.Errorf("P2 LastSeen = %d, want %d", c.Members[1].LastSeen, now)
	}
	// Idempotent: applying the same role again is a no-op.
	if err := c.ApplyActiveRole("skygate-standby", now); err != nil {
		t.Errorf("idempotent ApplyActiveRole: %v", err)
	}
}

func TestHaChain_MarshalRoundTrip(t *testing.T) {
	original := &HaChain{
		Members: []HaMember{
			{Hostname: "skygate", Priority: 1, PublicIP: "1.2.3.4", TailscaleIP: "100.64.0.1", LastSeen: 12345, Role: RoleActive},
			{Hostname: "skygate-standby", Priority: 2, PublicIP: "5.6.7.8", TailscaleIP: "100.64.0.2", LastSeen: 12346, Role: RoleStandby},
		},
		AutoFailoverEnabled: true,
		LastTransitionUnix:  99999,
	}
	bytes, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	parsed, err := UnmarshalChain(bytes)
	if err != nil {
		t.Fatalf("UnmarshalChain: %v", err)
	}
	if len(parsed.Members) != 2 {
		t.Fatalf("round-trip: got %d members, want 2", len(parsed.Members))
	}
	if parsed.Members[0].Hostname != "skygate" {
		t.Errorf("round-trip: P1 hostname = %q, want skygate", parsed.Members[0].Hostname)
	}
	if parsed.Members[1].Role != RoleStandby {
		t.Errorf("round-trip: P2 role = %q, want standby", parsed.Members[1].Role)
	}
	if !parsed.AutoFailoverEnabled {
		t.Errorf("round-trip: AutoFailoverEnabled = false, want true")
	}
}

func TestUnmarshalChain_EmptyIsOK(t *testing.T) {
	c, err := UnmarshalChain(nil)
	if err != nil {
		t.Fatalf("UnmarshalChain(nil): %v", err)
	}
	if len(c.Members) != 0 {
		t.Errorf("expected empty chain, got %d members", len(c.Members))
	}
	c, err = UnmarshalChain([]byte{})
	if err != nil {
		t.Fatalf("UnmarshalChain([]byte{}): %v", err)
	}
	if len(c.Members) != 0 {
		t.Errorf("expected empty chain, got %d members", len(c.Members))
	}
}

func TestUnmarshalChain_RejectsBadJSON(t *testing.T) {
	_, err := UnmarshalChain([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestUnmarshalChain_RejectsInvalidChain(t *testing.T) {
	// Two actives — passes JSON parsing but fails Validate.
	bad := []byte(`{"members":[{"hostname":"a","priority":1,"role":"active"},{"hostname":"b","priority":2,"role":"active"}]}`)
	_, err := UnmarshalChain(bad)
	if err == nil {
		t.Fatal("expected Validate error")
	}
}

func TestHaChain_RoleFor(t *testing.T) {
	c := &HaChain{Members: []HaMember{
		{Hostname: "skygate", Role: RoleActive},
		{Hostname: "skygate-standby", Role: RoleStandby},
	}}
	if got := c.RoleFor("skygate", true); got != RoleActive {
		t.Errorf("RoleFor(skygate) = %q, want active", got)
	}
	if got := c.RoleFor("skygate-standby", true); got != RoleStandby {
		t.Errorf("RoleFor(skygate-standby) = %q, want standby", got)
	}
	if got := c.RoleFor("missing", false); got != RoleUnknown {
		t.Errorf("RoleFor(missing) = %q, want unknown", got)
	}
}

func TestHaChain_JSONShape(t *testing.T) {
	// Sanity check: the JSON field names match the
	// "operator-visible" contract. If we ever rename a
	// field, this test will catch it.
	c := &HaChain{Members: []HaMember{{Hostname: "skygate", Priority: 1}}}
	bytes, _ := json.Marshal(c)
	s := string(bytes)
	for _, want := range []string{`"hostname":"skygate"`, `"priority":1`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q: %s", want, s)
		}
	}
}
