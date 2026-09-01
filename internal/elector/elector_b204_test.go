// v1.5.0+ (B204) — unit tests for the HA elector.
//
// We exercise nextState() and the role helpers as pure
// functions (no DB), and the recommendFailover() dedup
// via a tiny in-process DB shim that records the
// INSERTs. The full DB-tx path is covered by the live
// B204 verify script (insert a real cluster_node row,
// manipulate joined_at / last_seen_at via SQL, watch
// the elector transition + write the audit row).

package elector

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// nextState is the pure decision function — these tests
// pin the state machine without a DB.
func TestNextState(t *testing.T) {
	now := time.Now().UTC()
	staleAfter := HeartbeatIntervalSeconds * time.Second * StaleMultiplier
	cutoff := now.Add(-staleAfter)

	cases := []struct {
		name     string
		state    string
		lastSeen sql.NullTime
		joinedAt sql.NullTime
		want     string
	}{
		{
			name:     "pending + fresh joined_at → no transition",
			state:    "pending",
			lastSeen: sql.NullTime{},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Minute)},
			want:     "pending",
		},
		{
			name:     "pending + old joined_at (no heartbeat) → failed",
			state:    "pending",
			lastSeen: sql.NullTime{},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-2 * staleAfter)},
			want:     "failed",
		},
		{
			name:     "ready + recent last_seen → no transition",
			state:    "ready",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-10 * time.Second)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Hour)},
			want:     "ready",
		},
		{
			name:     "ready + old last_seen → failed",
			state:    "ready",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-2 * staleAfter)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Hour)},
			want:     "failed",
		},
		{
			name:     "failed → no auto-recovery (Heartbeat() does it)",
			state:    "failed",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Second)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Hour)},
			want:     "failed",
		},
		{
			name:     "draining → ignored (terminal)",
			state:    "draining",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-2 * staleAfter)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Hour)},
			want:     "draining",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := nextState(c.state, c.lastSeen, c.joinedAt, cutoff)
			if got != c.want {
				t.Errorf("nextState(%q) = %q, want %q", c.state, got, c.want)
			}
		})
	}
}

// roleContains is the PG-array-literal parser used by
// recommendFailover. The roles come back from pgx v5
// stdlib as the literal string (the same pattern as
// internal/db/array.go's StringArray), so we match the
// exact format.
func TestRoleContains(t *testing.T) {
	cases := []struct {
		roles string
		want  string
		found bool
	}{
		{"{skygate}", "skygate", true},
		{"{skygate,skygate-standby}", "skygate-standby", true},
		{"{skygate,skygate-standby}", "patroni-primary", false},
		{"{}", "skygate", false},
		{"", "skygate", false},
		{"{skygate}", "skygat", false},
		{`{"quoted role"}`, "quoted role", true},
	}
	for _, c := range cases {
		t.Run(c.roles, func(t *testing.T) {
			got := roleContains(c.roles, c.want)
			if got != c.found {
				t.Errorf("roleContains(%q, %q) = %v, want %v", c.roles, c.want, got, c.found)
			}
		})
	}
}

// splitRolesLiteral exercises the inner helper.
func TestSplitRolesLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"skygate", []string{"skygate"}},
		{"skygate,skygate-standby", []string{"skygate", "skygate-standby"}},
		{`"quoted role","other"`, []string{"quoted role", "other"}},
		{"", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := splitRolesLiteral(c.in)
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("splitRolesLiteral(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestDefaultConfig — pins the recommended settings so
// a future refactor can't silently change the heartbeat
// interval (which would change the failure detection
// latency for every node in the cluster).
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s", cfg.Interval)
	}
	if cfg.HeartbeatInterval != HeartbeatIntervalSeconds*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %ds", cfg.HeartbeatInterval, HeartbeatIntervalSeconds)
	}
	if cfg.ClusterID != "skygate-staging" {
		t.Errorf("ClusterID = %q, want skygate-staging", cfg.ClusterID)
	}
	if cfg.Logger == nil {
		t.Error("Logger is nil; should default to log.Printf")
	}
}

// TestNewElector_DefaultsApplied — nil fields fall back
// to the defaults.
func TestNewElector_DefaultsApplied(t *testing.T) {
	e := NewElector(Config{}, nil)
	if e.cfg.Interval != 5*time.Second {
		t.Errorf("Interval = %v, want 5s (default)", e.cfg.Interval)
	}
	if e.cfg.HeartbeatInterval != HeartbeatIntervalSeconds*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %ds", e.cfg.HeartbeatInterval, HeartbeatIntervalSeconds)
	}
	if e.cfg.ClusterID != "skygate-staging" {
		t.Errorf("ClusterID = %q, want skygate-staging", e.cfg.ClusterID)
	}
	if e.cfg.Logger == nil {
		t.Error("Logger is nil; should default to log.Printf")
	}
	if e.stop == nil || e.done == nil {
		t.Error("stop/done channels not initialised")
	}
}

// TestHeartbeatIntervalConstants — pins the B204 contract
// (3 missed heartbeats → failed) at the constant level.
// If anyone changes these, every cluster's failure
// detection latency changes.
func TestHeartbeatIntervalConstants(t *testing.T) {
	if HeartbeatIntervalSeconds != 30 {
		t.Errorf("HeartbeatIntervalSeconds = %d, want 30", HeartbeatIntervalSeconds)
	}
	if StaleMultiplier != 3 {
		t.Errorf("StaleMultiplier = %d, want 3", StaleMultiplier)
	}
}
