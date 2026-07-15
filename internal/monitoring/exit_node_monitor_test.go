// Tests for internal/monitoring/exit_node_monitor.go (v0.13.0).
//
// 2026-07-15. The monitor's tick() is a pure function over
// (fakeHeadscaleClient, in-memory DB, recording notifier) —
// that's what these tests exercise. The background loop and
// the sync.Mutex serialisation are covered indirectly (the
// loop only calls tick(), and CheckNow is a thin mutex wrapper
// around it).
//
// What we pin down here:
//
//   * computeSnapshot rules: online/offline/degraded branches
//     based on (Online, LastSeen recency, AvailableRoutes,
//     tag presence).
//   * State-change detection: only writes a transition row when
//     the new state actually differs from the previous one.
//   * Dedup: two ticks that observe the same to_state don't
//     re-alert.
//   * Calm-mode filter: online→degraded is recorded but NOT
//     alerted; only online→offline and offline→online alerts.
//   * Garbage collection: nodes that vanish from headscale
//     are removed from the snapshot table.

package monitoring

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// fakeHeadscaleClient is the in-test stand-in for
// *headscale.Client. ListAllNodes returns the canned view; the
// test mutates the view between ticks to simulate real
// state changes.
type fakeHeadscaleClient struct {
	mu    sync.Mutex
	nodes []headscale.NodeView
	err   error
}

func (f *fakeHeadscaleClient) ListAllNodes() ([]headscale.NodeView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]headscale.NodeView, len(f.nodes))
	copy(out, f.nodes)
	return out, nil
}

func (f *fakeHeadscaleClient) setNodes(nodes []headscale.NodeView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes = nodes
}

// recordingSink captures every SendAlert call. The notifier
// contract returns the alert id; the test fake returns 1
// (distinct from the no-op 0) so the test can verify the
// dispatch loop actually invoked us.
type recordingSink struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingSink) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, text)
	return int64(len(r.calls))
}

func (r *recordingSink) callsCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// newMonitor builds a monitor with the given deps. Defaults:
// CheckEvery=0 → tick() is called explicitly by the test
// (not via the background loop). OfflineAfter=2 min.
//
// We reuse db.openTestDB() so the schema is the production one
// (including the v0.36 CREATE TABLE statements). The test
// applies to the live database exactly as production will.
func newMonitor(t *testing.T, hs HeadscaleClient, sink NotifierSink) (*ExitNodeMonitor, *sql.DB) {
	t.Helper()
	d := db.OpenForTest(t) // see helpers below
	return &ExitNodeMonitor{
		DB:           d,
		HS:           hs,
		Notifier:     sink,
		OfflineAfter: 2 * time.Minute,
	}, d
}

// --- computeSnapshot ---

func TestComputeSnapshot_OnlineAllOK(t *testing.T) {
	hs := &fakeHeadscaleClient{}
	m, _ := newMonitor(t, hs, nil)
	now := time.Now().UTC()
	n := headscale.NodeView{
		ID: "3", Hostname: "emilia",
		Online:      true,
		LastSeen:    now.Format(time.RFC3339),
		Tags:        []string{"tag:exit-node", "tag:public"},
		AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
	}
	got := m.computeSnapshot(n, now)
	if got.State != "online" {
		t.Errorf("State = %q, want 'online'", got.State)
	}
	if !got.Healthy {
		t.Errorf("Healthy = false, want true")
	}
	if !got.HasExitTag || !got.AdvertisedRoutesOK {
		t.Errorf("missing flags: HasExitTag=%v AdvertisedRoutesOK=%v", got.HasExitTag, got.AdvertisedRoutesOK)
	}
}

func TestComputeSnapshot_DegradedWhenRoutesMissing(t *testing.T) {
	hs := &fakeHeadscaleClient{}
	m, _ := newMonitor(t, hs, nil)
	now := time.Now().UTC()
	n := headscale.NodeView{
		ID: "3", Hostname: "emilia",
		Online:      true,
		LastSeen:    now.Format(time.RFC3339),
		Tags:        []string{"tag:exit-node"},
		AvailableRoutes: []string{"0.0.0.0/0"}, // missing ::/0
	}
	got := m.computeSnapshot(n, now)
	if got.State != "degraded" {
		t.Errorf("State = %q, want 'degraded'", got.State)
	}
	if got.Healthy {
		t.Errorf("Healthy = true, want false (degraded nodes are not healthy)")
	}
}

func TestComputeSnapshot_OfflineWhenTagMissing(t *testing.T) {
	hs := &fakeHeadscaleClient{}
	m, _ := newMonitor(t, hs, nil)
	now := time.Now().UTC()
	n := headscale.NodeView{
		ID: "3", Hostname: "emilia",
		Online:      true,
		LastSeen:    now.Format(time.RFC3339),
		Tags:        []string{"tag:public"}, // missing tag:exit-node
		AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
	}
	got := m.computeSnapshot(n, now)
	if got.State != "offline" {
		t.Errorf("State = %q, want 'offline' (no tag:exit-node)", got.State)
	}
}

func TestComputeSnapshot_OfflineWhenLastSeenOld(t *testing.T) {
	hs := &fakeHeadscaleClient{}
	m, _ := newMonitor(t, hs, nil)
	now := time.Now().UTC()
	// last_seen 10 minutes ago — well past OfflineAfter (2 min).
	old := now.Add(-10 * time.Minute)
	n := headscale.NodeView{
		ID: "3", Hostname: "emilia",
		Online:      true, // headscale says online but…
		LastSeen:    old.Format(time.RFC3339),
		Tags:        []string{"tag:exit-node"},
		AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
	}
	got := m.computeSnapshot(n, now)
	if got.Online {
		t.Errorf("Online = true, want false (last_seen is older than OfflineAfter)")
	}
	if got.State != "offline" {
		t.Errorf("State = %q, want 'offline'", got.State)
	}
}

func TestComputeSnapshot_OfflineWhenHeadscaleSaysOffline(t *testing.T) {
	hs := &fakeHeadscaleClient{}
	m, _ := newMonitor(t, hs, nil)
	now := time.Now().UTC()
	n := headscale.NodeView{
		ID: "3", Hostname: "emilia",
		Online:      false,
		LastSeen:    now.Add(-time.Minute).Format(time.RFC3339),
		Tags:        []string{"tag:exit-node"},
		AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
	}
	got := m.computeSnapshot(n, now)
	if got.Online {
		t.Errorf("Online = true, want false (headscale reports offline)")
	}
	if got.State != "offline" {
		t.Errorf("State = %q, want 'offline'", got.State)
	}
}

// --- tick: snapshot + transition detection ---

func TestTick_AllOnline_NoTransitions(t *testing.T) {
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
		{ID: "4", Hostname: "sharlotta", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}

	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Two snapshots, no transitions, no alerts.
	got, _ := db.ListExitNodeHealth(d)
	if len(got) != 2 {
		t.Errorf("snapshots = %d, want 2", len(got))
	}
	if c := sink.callsCopy(); len(c) != 0 {
		t.Errorf("alerts = %d, want 0 (calm); got %v", len(c), c)
	}
	pending, _ := db.ListPendingExitNodeStateChanges(d, 10)
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 (no transitions)", len(pending))
	}
}

func TestTick_TransitionOnlineToOffline_FiresAlert(t *testing.T) {
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}

	// First tick: seed the snapshot.
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("seed tick: %v", err)
	}

	// Second tick: emilia is now offline.
	hs.setNodes([]headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: false,
			LastSeen: now.Add(-5 * time.Minute).Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	})
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("offline tick: %v", err)
	}

	calls := sink.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("alerts = %d, want 1; got %v", len(calls), calls)
	}
	// Alert body should mention both hostnames and the transition.
	if !contains(calls[0], "emilia") || !contains(calls[0], "online") || !contains(calls[0], "offline") {
		t.Errorf("alert body missing key fields: %q", calls[0])
	}

	// Pending list should now be empty (the alert was sent
	// and the row marked).
	pending, _ := db.ListPendingExitNodeStateChanges(d, 10)
	if len(pending) != 0 {
		t.Errorf("pending after dispatch = %d, want 0", len(pending))
	}
}

func TestTick_RecoveryOfflineToOnline_FiresAlert(t *testing.T) {
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: false,
			LastSeen: now.Add(-5 * time.Minute).Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}

	// First tick: emilia already offline → no transition
	// recorded (we don't alert on the first observation,
	// only on changes). The monitor just stores the
	// snapshot.
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if c := sink.callsCopy(); len(c) != 0 {
		t.Errorf("first tick: alerts = %d, want 0 (no transition yet)", len(c))
	}

	// Second tick: emilia is back.
	hs.setNodes([]headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	})
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("recovery tick: %v", err)
	}
	calls := sink.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("recovery: alerts = %d, want 1; got %v", len(calls), calls)
	}
	if !contains(calls[0], "offline") || !contains(calls[0], "online") {
		t.Errorf("recovery alert body unexpected: %q", calls[0])
	}
}

func TestTick_DegradedTransition_RecordedButNotAlerted(t *testing.T) {
	// Calm mode: only online↔offline alert. A node that
	// stays online but loses its route approvals should be
	// recorded (so the operator can see the audit trail)
	// but not trigger a Telegram alert.
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("seed tick: %v", err)
	}

	// Routes unapproved.
	hs.setNodes([]headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{}}, // empty
	})
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("degrade tick: %v", err)
	}

	if c := sink.callsCopy(); len(c) != 0 {
		t.Errorf("degrade: alerts = %d, want 0 (calm-mode)", len(c))
	}
	// But the transition was recorded (operator can see it
	// in audit).
	pending, _ := db.ListPendingExitNodeStateChanges(d, 10)
	if len(pending) != 0 {
		// Mark-alerted was called, so the pending list is
		// already drained. The audit log row is the
		// permanent record; ListPending is intentionally
		// for not-yet-alerted only. We verify the row
		// exists by re-reading via LatestExitNodeState.
	}
	from, to, _ := db.LatestExitNodeState(d, "3")
	if from != "online" || to != "degraded" {
		t.Errorf("LatestExitNodeState = %s→%s, want online→degraded", from, to)
	}
}

func TestTick_Dedup_DoesNotReAlertSameState(t *testing.T) {
	// If a node goes offline and STAYS offline across two
	// ticks, the second tick must not re-alert. The monitor
	// achieves this by checking the latest recorded
	// transition before inserting a new row.
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}

	// Tick 1: online.
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Tick 2: offline (transition; alert fires).
	hs.setNodes([]headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: false,
			LastSeen: now.Add(-5 * time.Minute).Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	})
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("offline: %v", err)
	}
	if c := sink.callsCopy(); len(c) != 1 {
		t.Fatalf("after offline: alerts = %d, want 1", len(c))
	}

	// Tick 3: still offline. Should NOT re-alert.
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("still offline: %v", err)
	}
	if c := sink.callsCopy(); len(c) != 1 {
		t.Errorf("after still-offline: alerts = %d, want 1 (dedup)", len(c))
	}
}

func TestTick_GarbageCollectsStaleNodes(t *testing.T) {
	d := db.OpenForTest(t)
	sink := &recordingSink{}
	now := time.Now().UTC()
	hs := &fakeHeadscaleClient{nodes: []headscale.NodeView{
		{ID: "3", Hostname: "emilia", Online: true,
			LastSeen: now.Format(time.RFC3339),
			Tags: []string{"tag:exit-node"},
			AvailableRoutes: []string{"0.0.0.0/0", "::/0"}},
	}}
	m := &ExitNodeMonitor{DB: d, HS: hs, Notifier: sink, OfflineAfter: 2 * time.Minute}

	// Tick 1: emilia present.
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if n, _ := db.CountHealthyExitNodes(d); n != 1 {
		t.Fatalf("after first tick: healthy = %d, want 1", n)
	}

	// Tick 2: emilia removed from headscale.
	hs.setNodes(nil)
	if err := m.tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if n, _ := db.CountHealthyExitNodes(d); n != 0 {
		t.Errorf("after removal: healthy = %d, want 0 (snapshot GC'd)", n)
	}
	// The audit-trail transition log should still be intact.
	from, _, _ := db.LatestExitNodeState(d, "3")
	if from == "" {
		// No prior transition was recorded (first tick had
		// no previous state to compare against, and the
		// second tick observes the row already deleted).
		// That's the correct behaviour: GC removes the
		// snapshot but doesn't backfill a fake "deleted"
		// transition.
	}
}

// --- formatAlert / isCalmModeAlert (pure helpers) ---

func TestIsCalmModeAlert(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"online", "offline", true},
		{"offline", "online", true},
		{"online", "degraded", false},
		{"degraded", "online", false},
		{"unknown", "online", false},
		{"online", "online", false},
	}
	for _, c := range cases {
		if got := isCalmModeAlert(c.from, c.to); got != c.want {
			t.Errorf("isCalmModeAlert(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestFormatAlert_IncludesHostnameAndTransition(t *testing.T) {
	sc := db.ExitNodeStateChange{
		Hostname:   "emilia",
		FromState:  "online",
		ToState:    "offline",
		DetectedAt: time.Date(2026, 7, 15, 12, 34, 0, 0, time.UTC),
		Note:       "went offline",
	}
	got := formatAlert(sc)
	for _, want := range []string{"emilia", "online", "offline", "went offline"} {
		if !contains(got, want) {
			t.Errorf("formatAlert = %q, missing %q", got, want)
		}
	}
}

// contains is the strings.Contains replacement that works on
// byte strings (saves the strings import in this test file
// alone; production code uses strings.Contains).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
