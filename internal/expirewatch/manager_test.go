package expirewatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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
//   - on POST /api/v1/node/{id}/expire returns 200 (the real API
//     is broken in headscale 0.29.2 — the watcher's
//     ExtendNodeExpiry goes straight to the CLI path)
//   - on `docker exec headscale headscale nodes expire -i N --expiry E`
//     records N in `renewed` and exits 0 (the actual production path
//     in headscale 0.29.2 — see ExtendNodeExpiry doc)
type fakeHS struct {
	srv     *httptest.Server
	c       *headscale.Client
	mu      sync.Mutex
	renewed []int64
}

func newFakeHS(t *testing.T, nodes []headscale.HSNode) *fakeHS {
	t.Helper()
	f := &fakeHS{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/node" && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		case strings.HasPrefix(r.URL.Path, "/api/v1/node/") &&
			strings.HasSuffix(r.URL.Path, "/expire") &&
			r.Method == "POST":
			// The watcher should NOT hit this path in
			// headscale 0.29.2 (REST API has a bug — see
			// ExtendNodeExpiry doc). We still return 200
			// so tests that DO hit the API path can verify
			// the watcher's behavior.
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "not found", 404)
		}
	}))
	f.c = headscale.New(f.srv.URL, "test-key")
	// Inject a stub dockerRunner that records the node ID
	// from `docker exec headscale headscale nodes expire -i N ...`.
	// (The real ExtendNodeExpiry uses this path; see
	// internal/headscale/nodes.go for why.)
	f.c.SetDockerRunner(func(args ...string) ([]byte, error) {
		// args shape: ["exec", "headscale", "headscale", "nodes", "expire", "-i", "N", "--expiry", "T"]
		var id int64
		for i, a := range args {
			if a == "-i" && i+1 < len(args) {
				id = parseInt64(args[i+1])
				break
			}
		}
		if id > 0 {
			f.mu.Lock()
			f.renewed = append(f.renewed, id)
			f.mu.Unlock()
		}
		return []byte("Node expiration updated\n"), nil
	})
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

// --- TestExpireWatch_SkipsOnlyNilExpiry (v0.23.4) ---

// v0.23.3 used to skip any tagged node — wrong, because a
// node can register untagged (and pick up the Tailscale
// 1.98.x 2-4s Expiry) and only be tagged later by skygate's
// backfill. v0.23.4 changed the rule to "skip only when
// Expiry is nil" so tag:private nodes like skybars /
// skybars-1 / Nothing Phone / Base get renewed.
//
// The 4 sub-cases:
//
//  1. tagged + nil Expiry       → skip  (emilia, sharlotta)
//  2. tagged + real Expiry      → renew (skybars-style bug)
//  3. untagged + nil Expiry     → skip  (operator --disable)
//  4. untagged + far Expiry     → skip  (already 30d out)
func TestExpireWatch_SkipsOnlyNilExpiry(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		// (1) tagged + nil — emilia-style exit-node.
		makeNode("11", "emilia", []string{"tag:exit-node", "tag:public"}, time.Time{}),
		// (2) tagged + 2s — skybars-style: tag:private
		// device that registered without a tag and
		// picked up the 2-4s client bug. Must renew.
		makeNode("10", "skybars", []string{"tag:private"}, now.Add(2*time.Second)),
		// (3) untagged + nil — operator ran --disable.
		// Must NOT renew (would override operator intent).
		makeNode("12", "disabled", nil, time.Time{}),
		// (4) untagged + 30d — already 30 days out.
		// Must NOT renew.
		makeNode("13", "fresh-30d", nil, now.Add(30*24*time.Hour)),
	}
	f := newFakeHS(t, nodes)
	mgr := New(openTestDB(t), f.c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	got := f.Renewed()
	if len(got) != 1 {
		t.Fatalf("renewed = %v, want 1 entry (only node 10, skybars-style)", got)
	}
	if got[0] != 10 {
		t.Errorf("renewed = %v, want [10] (tag:private with near expiry must be renewed)", got)
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

// --- TestExpireWatch_HandlesCLIFailure ---

func TestExpireWatch_HandlesCLIFailure(t *testing.T) {
	now := time.Now()
	nodes := []headscale.HSNode{
		makeNode("50", "will-fail", nil, now.Add(2*time.Second)),
	}
	// Fake HS that returns the node list but where the
	// dockerRunner always fails (simulates a headscale
	// container that's down or unreachable).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/node" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
			return
		}
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	c := headscale.New(srv.URL, "test-key")
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		return []byte("Error: no such container"), fmt.Errorf("exit status 1")
	})

	mgr := New(openTestDB(t), c, nil, time.Hour)
	mgr.Threshold = 7 * 24 * time.Hour
	mgr.Renewal = 30 * 24 * time.Hour
	mgr.SetAppendAudit(db.AppendAuditLog)

	// SyncOnce continues past per-node errors (per design:
	// one bad node shouldn't block the rest from being
	// renewed). The contract is that the tick's stats
	// reflect the failure, not that the overall SyncOnce
	// returns an error.
	_ = mgr.SyncOnce(context.Background())
	stats := mgr.LastStats()
	if stats.Errors == 0 {
		t.Errorf("stats.Errors = 0, want > 0 (one node failed to renew)")
	}
	if stats.Renewed != 0 {
		t.Errorf("stats.Renewed = %d, want 0 (failed renewal should not count as success)", stats.Renewed)
	}
}
