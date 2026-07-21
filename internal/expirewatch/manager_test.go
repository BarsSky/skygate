package expirewatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// openTestDB opens a fresh sqlite db with the full schema.
// Mirrors internal/sidecar/manager_test.go:openTestDB.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "expirewatch-test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// fakeHSServer returns a headscale-API stub that:
//   - on GET /api/v1/node returns the given list of nodes
//   - on POST /api/v1/node/{id}/expire records the call in `renewed` and returns 200
// The test can read renewed after SyncOnce to confirm the
// watcher called the right IDs.
type fakeHS struct {
	srv     *httptest.Server
	c       *headscale.Client
	mu      sync.Mutex
	renewed []int64 // node IDs that received a renew
}

func newFakeHS(t *testing.T, nodes []headscale.HSNode) *fakeHS {
	t.Helper()
	f := &fakeHS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/node" && r.Method == "GET":
			f.mu.Lock()
			defer f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		case strings.HasPrefix(r.URL.Path, "/api/v1/node/") &&
			strings.HasSuffix(r.URL.Path, "/expire") &&
			r.Method == "POST":
			// parse ID from path
			idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
			idStr = strings.TrimSuffix(idStr, "/expire")
			f.mu.Lock()
			// atomic counter via int64 slice append
			// (sync.Mutex protects the slice header)
			f.renewed = append(f.renewed, parseInt64(idStr))
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	f.c = headscale.New(f.srv.URL, "test-key")
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHS) Renewed() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.renewed))
	copy(out, f.renewed)
	return out
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// makeNode returns a headscale.HSNode with a 1h renewal in
// the future. Tests can override Expiry to simulate the
// "2-4 second client bug" or the "no expiry" state.
func makeNode(id string, name string, tags []string, expiry time.Time) headscale.HSNode {
	expStr := ""
	if !expiry.IsZero() {
		expStr = expiry.UTC().Format(time.RFC3339Nano)
	}
	return headscale.HSNode{
		ID:        id,
		Name:      name,
		GivenName: name,
		User:      headscale.HSUser{Name: "skyadmin", ID: "1"},
		Online:    true,
		LastSeen:  time.Now().Format(time.RFC3339),
		Expiry:    expStr,
		Tags:      tags,
	}
}

// --- TestExpireWatch_PicksOnlyNearExpiry ---

func TestExpireWatch_PicksOnlyNearExpiry(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		// 2-second expiry: matches the Tailscale 1.98.x
		// client bug. Must be renewed.
		makeNode("10", "skybars", nil, now.Add(2*time.Second)),
		// 30-day expiry: comfortably in the future. Must NOT
		// be renewed.
		makeNode("16", "agent", nil, now.Add(30*24*time.Hour)),
		// 5-day expiry: inside the 7-day threshold. Must be
		// renewed.
		makeNode("17", "agent-1", nil, now.Add(5*24*time.Hour)),
	}
	f := newFakeHS(t, nodes)
	mgr := New(openTestDB(t), f.c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour
	mgr.SetAppendAudit(db.AppendAuditLog)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	got := f.Renewed()
	if len(got) != 2 {
		t.Fatalf("renewed = %v, want 2 entries (nodes 10, 17)", got)
	}
	// Order isn't guaranteed by headscale's list; just check
	// the set.
	seen := make(map[int64]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	if !seen[10] {
		t.Error("node 10 (2s expiry) was not renewed")
	}
	if !seen[17] {
		t.Error("node 17 (5d expiry) was not renewed")
	}
	if seen[16] {
		t.Error("node 16 (30d expiry) should NOT have been renewed")
	}
}

// --- TestExpireWatch_SkipsTagged ---

func TestExpireWatch_SkipsTagged(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		// Tagged node with 2-second expiry. Must NOT be
		// renewed (headscale's state.go skips tagged
		// nodes for regReq.Expiry, so their natural
		// expiry is nil — a 2s value here is an
		// inconsistency that the watcher does not try
		// to "fix").
		makeNode("11", "emilia", []string{"tag:exit-node", "tag:public"}, now.Add(2*time.Second)),
	}
	f := newFakeHS(t, nodes)
	mgr := New(openTestDB(t), f.c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := f.Renewed(); len(got) != 0 {
		t.Errorf("renewed = %v, want 0 entries (tagged node should be skipped)", got)
	}
}

// --- TestExpireWatch_HandlesMissingExpiry ---

func TestExpireWatch_HandlesMissingExpiry(t *testing.T) {
	// Node with no expiry at all (empty string from headscale
	// API). The watcher renews defensively.
	nodes := []headscale.HSNode{
		makeNode("20", "no-expiry-node", nil, time.Time{}),
	}
	f := newFakeHS(t, nodes)
	mgr := New(openTestDB(t), f.c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := f.Renewed(); len(got) != 1 || got[0] != 20 {
		t.Errorf("renewed = %v, want [20] (defensive renewal for missing expiry)", got)
	}
}

// --- TestExpireWatch_RespectsIntervalZero (disabled) ---

func TestExpireWatch_RespectsIntervalZero(t *testing.T) {
	// Interval <= 0 must short-circuit Run: no calls to
	// HS.ListAllNodes at all. We verify this by passing a
	// nil HS client and expecting no panic.
	mgr := New(openTestDB(t), nil, nil, 0)
	mgr.Run(context.Background())
	stats := mgr.LastStats()
	if !stats.At.IsZero() {
		t.Errorf("stats.At = %v, want zero (Run should have short-circuited)", stats.At)
	}
}

// --- TestExpireWatch_RunStopsOnContextCancel ---

func TestExpireWatch_RunStopsOnContextCancel(t *testing.T) {
	d := openTestDB(t)
	// Fake HS so Run's first tick doesn't nil-ponic.
	f := newFakeHS(t, nil)
	// Use a slow tick so Run doesn't fire before cancel.
	mgr := New(d, f.c, nil, 1*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()
	// Give Run enough time to do its initial SyncOnce and
	// start the ticker.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// --- TestExpireWatch_RecordsAuditOnRenew ---

func TestExpireWatch_RecordsAuditOnRenew(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		makeNode("30", "audit-test", nil, now.Add(2*time.Second)),
	}
	f := newFakeHS(t, nodes)
	d := openTestDB(t)
	mgr := New(d, f.c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour
	mgr.SetAppendAudit(db.AppendAuditLog)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// Verify the audit row.
	row := d.QueryRow(`
		SELECT username, action, detail FROM audit_log
		 WHERE action = 'renewed' AND username = 'expirewatch'
		 ORDER BY id DESC LIMIT 1`)
	var username, action, detail string
	if err := row.Scan(&username, &action, &detail); err != nil {
		t.Fatalf("scan audit row: %v", err)
	}
	if username != "expirewatch" {
		t.Errorf("username = %q, want 'expirewatch'", username)
	}
	if action != "renewed" {
		t.Errorf("action = %q, want 'renewed'", action)
	}
	if !strings.Contains(detail, "node_id=30") {
		t.Errorf("detail = %q, want contains 'node_id=30'", detail)
	}
}

// --- TestExpireWatch_ParsesRFC3339NanoExpiry ---

func TestExpireWatch_ParsesRFC3339NanoExpiry(t *testing.T) {
	now := time.Now()
	raw := now.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)

	// Empty view: should report !hasExpiry.
	_, _, hasExpiry := nodeExpiryFromCache(headscale.NodeView{})
	if hasExpiry {
		t.Error("empty NodeView.Expiry should report !hasExpiry")
	}

	// Real RFC3339Nano expiry: should parse to ~now+2s.
	// Note: we compare via time.Until on a UTC-stamped expected
	// value, not time.Since which uses local-time monotonic
	// clock and confuses the assertion when local TZ != UTC.
	view := headscale.NodeView{Expiry: raw}
	_, expTime, hasExpiry := nodeExpiryFromCache(view)
	if !hasExpiry {
		t.Fatalf("expected hasExpiry=true for %q", raw)
	}
	rawExpected, _ := time.Parse(time.RFC3339Nano, raw)
	if diff := expTime.Sub(rawExpected); diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("expTime = %v, want %v (diff %v)", expTime, rawExpected, diff)
	}

	// RFC3339 (no fractional) must also parse.
	rawNoFrac := now.Add(3 * time.Second).UTC().Format(time.RFC3339)
	_, expTime2, hasExpiry2 := nodeExpiryFromCache(headscale.NodeView{Expiry: rawNoFrac})
	if !hasExpiry2 {
		t.Fatalf("expected hasExpiry=true for %q", rawNoFrac)
	}
	rawExpected2, _ := time.Parse(time.RFC3339, rawNoFrac)
	if diff := expTime2.Sub(rawExpected2); diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("expTime2 = %v, want %v (diff %v)", expTime2, rawExpected2, diff)
	}

	// Garbage string: should report !hasExpiry so the
	// watcher renews defensively.
	_, _, hasExpiry3 := nodeExpiryFromCache(headscale.NodeView{Expiry: "not-a-date"})
	if hasExpiry3 {
		t.Error("garbage Expiry should report !hasExpiry")
	}
}

// --- TestExpireWatch_HandlesAPIFailure ---

func TestExpireWatch_HandlesAPIFailure(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		makeNode("50", "will-fail", nil, now.Add(2*time.Second)),
	}
	// Use a fake that returns 500 for the expire endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/node" && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		case strings.HasSuffix(r.URL.Path, "/expire"):
			http.Error(w, "server error", 500)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	t.Cleanup(srv.Close)
	c := headscale.New(srv.URL, "test-key")
	// No CLI fallback (ExecContainer = ""), so any HTTP
	// failure surfaces as an error from SyncOnce.
	mgr := New(openTestDB(t), c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour
	mgr.SetAppendAudit(db.AppendAuditLog)

	// SyncOnce currently continues past per-node errors
	// (per design: one bad node shouldn't block the rest
	// from being renewed). The contract is that the
	// tick's stats reflect the failure, not that the
	// overall SyncOnce returns an error.
	_ = mgr.SyncOnce(context.Background())
	stats := mgr.LastStats()
	if stats.Errors == 0 {
		t.Errorf("stats.Errors = 0, want > 0 (one node failed to renew)")
	}
	if stats.Renewed != 0 {
		t.Errorf("stats.Renewed = %d, want 0 (failed renewal should not count as success)", stats.Renewed)
	}
	// Race-detector friendly: confirm the goroutines
	// didn't panic by reading an atomic counter (none
	// used here, but the test reaching this point
	// without a panic is itself the assertion).
	_ = atomic.LoadInt32
}
