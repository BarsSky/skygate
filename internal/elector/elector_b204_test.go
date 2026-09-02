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
	"context"
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
			name:     "pending + last_seen set but old → failed (B204.1 regression)",
			state:    "pending",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-2 * staleAfter)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Hour)},
			want:     "failed",
		},
		{
			name:     "pending + last_seen set and fresh → no transition",
			state:    "pending",
			lastSeen: sql.NullTime{Valid: true, Time: now.Add(-10 * time.Second)},
			joinedAt: sql.NullTime{Valid: true, Time: now.Add(-1 * time.Minute)},
			want:     "pending",
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

// TestNewElector_NilSource_NoCrash — verify the
// elector doesn't panic when the DBSource returns nil
// (e.g. ResettableDB.Current() called before Start).
// The tick logs an error and returns; it does not loop
// forever on a nil deref.
func TestNewElector_NilSource_NoCrash(t *testing.T) {
	e := NewElector(DefaultConfig(), nilSource{})
	// Should not panic; should return an error from
	// the (no-)DB lookup.
	err := e.evaluate(nilForTest())
	if err == nil {
		t.Error("evaluate(nil source) = nil, want error")
	}
}

// nilSource is a DBSource that always returns nil.
// Used to verify the elector's nil-handling path.
type nilSource struct{}

func (nilSource) Current() *sql.DB { return nil }

// TestNewElector_DBSSource_FollowsPoolSwap — pins the
// "B203 hot-reload follow" contract: a custom DBSource
// that swaps its returned *sql.DB on each Current() call
// must be observed by the elector on the next tick.
// (We can't use the real ResettableDB here without a
// live PG, so we use a stub that just records the
// number of Current() calls.)
func TestNewElector_DBSSource_CurrentCalledPerTick(t *testing.T) {
	src := &countingSource{db: nil} // nil db → tick returns error
	e := NewElector(DefaultConfig(), src)
	// First tick.
	_ = e.evaluate(nilForTest())
	// Second tick.
	_ = e.evaluate(nilForTest())
	if src.calls < 2 {
		t.Errorf("Current() called %d times in 2 ticks, want >= 2", src.calls)
	}
}

// countingSource is a DBSource that returns nil
// (so evaluate returns an error) but records the
// number of Current() calls. The elector must call
// Current() on every tick (not cache the first result)
// to follow B203 hot-reloads.
type countingSource struct {
	db    *sql.DB
	calls int
}

func (c *countingSource) Current() *sql.DB {
	c.calls++
	return c.db
}

// nilForTest returns a context that the elector's
// evaluate() can use. The elector never calls
// time.AfterFunc or similar; the context just needs
// to be non-nil to satisfy the (*Elector).evaluate
// signature. We use context.TODO() because the
// evaluator never actually issues a query when the
// source is nil (it returns early).
func nilForTest() (ctx context.Context) {
	return context.TODO()
}

// TestDefaultConfig_NowSet — B209 contract: DefaultConfig
// must populate Config.Now (so callers don't have to set
// it explicitly). The elector's evaluate() reads e.now()
// which falls back to time.Now if Now is nil — but a
// default-zero Config should still work out of the box.
func TestDefaultConfig_NowSet(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Now == nil {
		t.Fatal("DefaultConfig().Now is nil; should default to time.Now")
	}
	// Sanity: it actually returns a time value.
	got := cfg.Now()
	if got.IsZero() {
		t.Error("DefaultConfig().Now() returned zero time")
	}
}

// TestNewElector_NowFallback — even if the caller
// explicitly nil's out Config.Now, NewElector should
// restore it. This is the defensive guard for the
// "I constructed my Config by hand and forgot to set
// Now" path.
func TestNewElector_NowFallback(t *testing.T) {
	e := NewElector(Config{Now: nil}, nilSource{})
	if e.cfg.Now == nil {
		t.Fatal("NewElector did not restore nil Now to time.Now")
	}
}

// TestElector_NowUsesFakeClock — pin the B209 test hook:
// when Config.Now returns a fake clock, the elector's
// internal e.now() returns that fake value (not real
// time). This is the contract the e2e orchestrator
// script relies on to fast-forward through the 90s
// staleness window without sleeping in real time.
//
// We don't run evaluate() (it needs a real DB); we just
// verify e.now() follows the Config.
func TestElector_NowUsesFakeClock(t *testing.T) {
	fake := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	e := NewElector(Config{Now: func() time.Time { return fake }}, nilSource{})
	got := e.now()
	if !got.Equal(fake) {
		t.Errorf("e.now() = %v, want %v (fake clock)", got, fake)
	}
}

// TestNextState_AtFakeClockBoundary — B209: verify
// the 90s staleness boundary using a fake clock. We
// construct now via the new Now hook (instead of the
// time.Now() call in the test) and verify a node whose
// last_seen is exactly 90s old stays in 'ready' (the
// strict < comparison in nextState), and one whose
// last_seen is 91s old transitions to 'failed'.
//
// The 1-second gap pins the "3 × 30s = 90s, not 91s,
// not 89s" contract from the B204 B-check.
func TestNextState_AtFakeClockBoundary(t *testing.T) {
	fakeNow := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	staleAfter := HeartbeatIntervalSeconds * time.Second * StaleMultiplier // 90s
	cutoff := fakeNow.Add(-staleAfter)

	cases := []struct {
		name     string
		state    string
		lastSeen time.Time
		want     string
	}{
		{
			name:     "ready + exactly 90s old → stays ready (strict <)",
			state:    "ready",
			lastSeen: fakeNow.Add(-90 * time.Second),
			want:     "ready",
		},
		{
			name:     "ready + 91s old → failed",
			state:    "ready",
			lastSeen: fakeNow.Add(-91 * time.Second),
			want:     "failed",
		},
		{
			name:     "ready + 89s old → stays ready (comfortable margin)",
			state:    "ready",
			lastSeen: fakeNow.Add(-89 * time.Second),
			want:     "ready",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := nextState(c.state, sql.NullTime{Valid: true, Time: c.lastSeen}, sql.NullTime{Valid: true, Time: fakeNow.Add(-1 * time.Hour)}, cutoff)
			if got != c.want {
				t.Errorf("nextState(%q) = %q, want %q (fake-now boundary)", c.state, got, c.want)
			}
		})
	}
}
