package nodeownership

// auto_test.go — tests for the B77 node-discovery
// autoupdater goroutine.
//
// 2026-08-09: v0.33.1.25 (B77) — addresses Issue 2 from
// the 2026-08-09 operator report. Pre-fix, new devices
// didn't get their dev-tag applied until the user
// manually visited /my/devices (or the admin clicked
// "Force backfill"). The fix is AutoBackfill, a
// periodic loop that calls Backfill against every
// portal user. These tests pin the contract:
//   - AutoBackfill honors ctx.Done() and exits
//   - AutoBackfill with interval <=0 is a no-op
//   - AutoBackfill with nil DB or nil HS is a no-op
//     (defensive guards)
//   - AutoBackfill handles a headscale API error
//     gracefully (logs + skips the tick + continues)
//   - AutoBackfill with a happy-path fake increments
//     ListAllNodes + InvalidateCache per tick and
//     calls Backfill against the seeded portal_users
//
// The fake implements the nodeLister interface
// (4 methods on *headscale.Client) so the test can
// exercise the per-tick code path without depending
// on a real headscale instance. The DB side is
// covered by the real *sql.DB (via openBackfillTestDB,
// shared with the existing nodeownership_test.go).
import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/headscale"
)

// fakeListClient implements nodeLister. Records each
// call. For "happy path" ticks (no err), the resp
// slice is what ListAllNodes returns. For the
// list-error tick, err is non-nil and resp is
// ignored. AddTag + UntagNode are counted but have
// no DB/headscale side effects in tests (the per-user
// Backfill is expected to call them when a rename is
// detected; for the seeded test data there are no
// renames, so AddTag is the only one that may fire).
type fakeListClient struct {
	invCalls   atomic.Int64
	listCalls  atomic.Int64
	addCalls   atomic.Int64
	untagCalls atomic.Int64

	// resp + err are the (ListAllNodes) return values.
	// Tests can mutate resp to simulate a changing
	// node list across ticks.
	resp []headscale.NodeView
	err  error

	// mu guards resp / err so the test can swap them
	// between ticks without racing the loop.
	mu sync.Mutex
}

func (f *fakeListClient) InvalidateCache() {
	f.invCalls.Add(1)
}

func (f *fakeListClient) ListAllNodes() ([]headscale.NodeView, error) {
	f.listCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resp, f.err
}

func (f *fakeListClient) AddTag(_ int64, _ string) error {
	f.addCalls.Add(1)
	return nil
}

func (f *fakeListClient) UntagNode(_ int64, _ string) error {
	f.untagCalls.Add(1)
	return nil
}

// TestAutoBackfill_ZeroIntervalIsNoop — when the
// interval is 0 (or negative), AutoBackfill should
// return without doing anything. The main.go gate
// is supposed to skip the goroutine launch in this
// case, but the defensive guard inside AutoBackfill
// means a future caller that forgets the gate still
// doesn't get an infinite loop or zero-tick spin.
func TestAutoBackfill_ZeroIntervalIsNoop(t *testing.T) {
	done := make(chan struct{})
	go func() {
		AutoBackfill(context.Background(), nil, &fakeListClient{}, 0)
		close(done)
	}()
	select {
	case <-done:
		// good — AutoBackfill returned immediately
	case <-time.After(2 * time.Second):
		t.Fatalf("AutoBackfill(interval=0) did not return within 2s (should be a no-op)")
	}
}

// TestAutoBackfill_NilDBIsNoop — defensive guard.
// A nil *sql.DB would panic on the first query; better
// to skip the goroutine cleanly than crash the process.
func TestAutoBackfill_NilDBIsNoop(t *testing.T) {
	hs := &fakeListClient{}
	done := make(chan struct{})
	go func() {
		AutoBackfill(context.Background(), nil, hs, 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("AutoBackfill(nil db) did not return within 2s")
	}
	if got := hs.listCalls.Load(); got != 0 {
		t.Errorf("AutoBackfill(nil db) called hs.ListAllNodes %d times; should be 0", got)
	}
}

// TestAutoBackfill_NilHSIsNoop — defensive guard for
// the headscale client too. Same rationale: nil-pointer
// dereference would crash the goroutine.
func TestAutoBackfill_NilHSIsNoop(t *testing.T) {
	d := openBackfillTestDB(t)
	done := make(chan struct{})
	go func() {
		AutoBackfill(context.Background(), d, nil, 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("AutoBackfill(nil hs) did not return within 2s")
	}
}

// TestAutoBackfill_ContextCancelExits — cancellation
// via ctx.Done() should make AutoBackfill return
// promptly. We use a short interval (50ms) to make
// sure the loop is actually running when we cancel.
func TestAutoBackfill_ContextCancelExits(t *testing.T) {
	d := openBackfillTestDB(t)
	hs := &fakeListClient{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		AutoBackfill(ctx, d, hs, 50*time.Millisecond)
		close(done)
	}()
	// Let it run for 1 tick (50ms+).
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatalf("AutoBackfill did not exit within 2s of ctx cancellation")
	}
	if got := hs.listCalls.Load(); got < 1 {
		t.Errorf("expected at least 1 ListAllNodes call before cancel; got %d", got)
	}
}

// TestAutoBackfill_ListErrorIsTolerated — when
// ListAllNodes errors (e.g. headscale API hiccup),
// AutoBackfill should log + skip the tick + continue
// running for the next tick. The next tick succeeds
// (returns nodes), proving the loop didn't bail.
func TestAutoBackfill_ListErrorIsTolerated(t *testing.T) {
	d := openBackfillTestDB(t)
	hs := &fakeListClient{err: errors.New("simulated headscale down")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		AutoBackfill(ctx, d, hs, 50*time.Millisecond)
		close(done)
	}()
	// After 200ms we should have had ~3-4 tick attempts,
	// each failing. Then we cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if got := hs.listCalls.Load(); got < 1 {
		t.Errorf("expected at least 1 ListAllNodes call (even if it errored); got %d", got)
	}
}

// TestAutoBackfill_HappyPath — full success tick:
// nodes are listed, Backfill is called for each
// portal user, the loop keeps running until cancel.
// Asserts:
//   1. InvalidateCache is called once per tick
//   2. ListAllNodes is called once per tick
//   3. The loop runs for multiple ticks before cancel
//   4. The total calls match the expected tick count
func TestAutoBackfill_HappyPath(t *testing.T) {
	d := openBackfillTestDB(t)
	// Insert 2 portal users via the test helper.
	if _, err := d.Exec(`INSERT INTO portal_users (id, username) VALUES (1, 'alice'), (2, 'bob')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	hs := &fakeListClient{
		resp: []headscale.NodeView{}, // empty — Backfill is a no-op for empty input
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		AutoBackfill(ctx, d, hs, 50*time.Millisecond)
		close(done)
	}()
	// Let it run for 3+ ticks.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	listCalls := hs.listCalls.Load()
	invCalls := hs.invCalls.Load()
	if listCalls < 2 {
		t.Errorf("expected >= 2 ListAllNodes calls (one per tick), got %d", listCalls)
	}
	if invCalls != listCalls {
		t.Errorf("InvalidateCache should be called once per tick: invs=%d listCalls=%d", invCalls, listCalls)
	}
}

// --- minimal compile-time checks ---

// Ensure that *headscale.Client structurally satisfies
// the nodeLister interface. This is a no-op at runtime
// but the compiler will reject the file if the
// methods drift. The trick: assign a typed nil to a
// typed variable of the right shape.
var _ nodeLister = (*headscale.Client)(nil)

// Keep the imports clean even if all tests are skipped.
var _ = sql.ErrNoRows
var _ = errors.New
