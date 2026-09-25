// auto_test.go — B77: the node-discovery autoupdater's real behaviour.
//
// B77 (v0.33.1.25) added the background goroutine that walks every portal user
// and back-fills node ownership, so a device registered with a skygate-issued
// preauth key gets its `tag:dev-<user>-<device>` without the user having to
// open /my/devices. The tick pipeline is
//
//	AutoBackfill (loop) → runOneTick → Backfill (per portal user)
//	                                 → BackfillInfra
//	                                 → RepairSentinelDeviceOwners
//	                                 → ReconcileTags
//
// History: the original TestAutoBackfill_* tests used the deleted
// openBackfillTestDB (hand-rolled SQLite schema) and were replaced by a t.Skip
// stub in the v1.3.0 SQLite→PG purge. The tests below pin the guards that exist
// TODAY, against a REAL migrated database (db.OpenWithDialect +
// db.ApplyMigrations) and a fake headscale client:
//
//   - interval <= 0 returns before the ticker is created (defense in depth; the
//     config-level gate — cfg.NodeDiscoveryInterval > 0 — lives in
//     cmd/skygate/main.go:3052, which a unit test cannot reach),
//   - a nil DBSource and a nil headscale client return instead of panicking,
//   - a cancelled context exits the loop without ticking,
//   - a tick really back-fills + reconciles, and cancelling stops the loop,
//   - a failing ListAllNodes skips the tick (the loop must survive a headscale
//     hiccup — availability over completeness).
//
// No network, no docker, no sleeps longer than the 1s wait budget.

package nodeownership

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// autoTestLister is a nodeLister that counts what the autoupdater asks of
// headscale. It is deliberately separate from fakeLister (auto_b272_test.go):
// these tests assert CALL COUNTS and the tick ordering, and mutating the shared
// B272 fake would couple the two test families.
//
// AddTag mutates the node list the way headscale would, so a second tick sees
// the tag already present and writes nothing — which keeps the "exactly one
// policy write" assertion deterministic even if a second tick fires before the
// cancellation is observed.
type autoTestLister struct {
	mu      sync.Mutex
	nodes   []headscale.NodeView
	listErr error

	invalidations int
	lists         int
	// tagged holds the tag SET applied per node: headscale accumulates tags
	// (AddTag is additive), and Backfill legitimately applies two different tags
	// to the same node (its dev-tag + tag:private).
	tagged       map[int64][]string
	tagOwners    map[string][]string
	ownerBatches int
	ownerCalls   int

	// tick receives a signal on every ListAllNodes call (buffered, non-blocking)
	// so a test can wait for a real tick without sleeping.
	tick chan struct{}
}

func newAutoTestLister(nodes ...headscale.NodeView) *autoTestLister {
	return &autoTestLister{
		nodes:     nodes,
		tagged:    map[int64][]string{},
		tagOwners: map[string][]string{},
	}
}

func (l *autoTestLister) InvalidateCache() {
	l.mu.Lock()
	l.invalidations++
	l.mu.Unlock()
}

func (l *autoTestLister) ListAllNodes() ([]headscale.NodeView, error) {
	l.mu.Lock()
	l.lists++
	nodes, err, tick := l.nodes, l.listErr, l.tick
	l.mu.Unlock()
	if tick != nil {
		select {
		case tick <- struct{}{}:
		default:
		}
	}
	return nodes, err
}

func (l *autoTestLister) AddTag(nodeID int64, tag string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !hasTag(l.tagged[nodeID], tag) {
		l.tagged[nodeID] = append(l.tagged[nodeID], tag)
	}
	for i := range l.nodes {
		if l.nodes[i].ID == strconv.FormatInt(nodeID, 10) && !hasTag(l.nodes[i].Tags, tag) {
			l.nodes[i].Tags = append(l.nodes[i].Tags, tag)
		}
	}
	return nil
}

func (l *autoTestLister) UntagNode(int64, string) error { return nil }

func (l *autoTestLister) EnsureTagOwner(tag string, owners []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ownerCalls++
	l.tagOwners[tag] = owners
	return nil
}

func (l *autoTestLister) EnsureTagOwners(wants map[string][]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ownerBatches++
	for tag, owners := range wants {
		l.tagOwners[tag] = owners
	}
	return nil
}

// counts returns the observed call counts in one snapshot.

func (l *autoTestLister) counts() (invalidations, lists, ownerBatches, ownerCalls int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.invalidations, l.lists, l.ownerBatches, l.ownerCalls
}

// tagsFor returns the tag set applied to a node by AddTag (in order).
func (l *autoTestLister) tagsFor(nodeID int64) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.tagged[nodeID]...)
}

func (l *autoTestLister) ownersOf(tag string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tagOwners[tag]
}

// newAutoTestDB opens a migrated in-memory SQLite database through the
// production open path. The DSN is a UNIQUELY named shared-cache database so it
// cannot share state with any other test in this process, and so a pooled
// second connection still sees the schema.
//
// The only skip is for an unavailable SQLite dialect (the pure-Go
// modernc.org/sqlite driver imported by internal/db) — never for a missing
// database, because the test creates its own.
func newAutoTestDB(t *testing.T) *skygatedb.FixedDBSource {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file:nodeownership_b77_auto?mode=memory&cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable (modernc.org/sqlite): %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &skygatedb.FixedDBSource{DB: d}
}

// seedAutoTestUser inserts one portal user and returns its id.
func seedAutoTestUser(t *testing.T, d *sql.DB, username string, hsID int64) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
		 VALUES (?, ?, 0, ?)`, username, "x", hsID)
	if err != nil {
		t.Fatalf("seed portal user %q: %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId for %q: %v", username, err)
	}
	return id
}

// seedAutoTestOwner inserts a node_owner_map row (the "database says this node
// is owned by X and carries tag Y" side of the B272 drift).
func seedAutoTestOwner(t *testing.T, d *sql.DB, nodeID string, hsID int64, username, tag, hostname string) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id, hostname)
		 VALUES (?, ?, ?, ?, 0, ?)`, nodeID, hsID, username, tag, hostname); err != nil {
		t.Fatalf("seed node_owner_map %s: %v", nodeID, err)
	}
}

// runAutoBackfill calls AutoBackfill in a goroutine and fails the test if it has
// not returned within 1s. Every guard under test must return synchronously; a
// positive interval keeps the loop alive until ctx is cancelled.
func runAutoBackfill(t *testing.T, ctx context.Context, dbConn skygatedb.DBSource, hs nodeLister, interval time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		AutoBackfill(ctx, dbConn, hs, nil, interval)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("AutoBackfill(interval=%v) did not return within 1s — the guard is gone and the goroutine started a ticker", interval)
	}
}

// TestAutoBackfill_NonPositiveIntervalIsANoOp pins the defense-in-depth guard:
// AutoBackfill itself refuses a zero/negative interval (main.go also refuses to
// call it, but a direct caller — a CLI path, a future second caller — must not
// be able to start a ticker with a non-positive duration, which panics).
func TestAutoBackfill_NonPositiveIntervalIsANoOp(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second, -time.Hour} {
		t.Run(interval.String(), func(t *testing.T) {
			src := newAutoTestDB(t)
			hs := newAutoTestLister()

			runAutoBackfill(t, context.Background(), src, hs, interval)

			invalidations, lists, batches, ownerCalls := hs.counts()
			if invalidations != 0 || lists != 0 || batches != 0 || ownerCalls != 0 {
				t.Errorf("interval=%v still touched headscale (invalidations=%d lists=%d tagOwnerBatches=%d tagOwnerCalls=%d)",
					interval, invalidations, lists, batches, ownerCalls)
			}
		})
	}
}

// TestAutoBackfill_NilDBSourceIsSafe: the DB handle can be nil during a
// half-initialised boot; the guard must log and return, never panic (a panic
// here kills the whole process — this goroutine is started from main).
func TestAutoBackfill_NilDBSourceIsSafe(t *testing.T) {
	hs := newAutoTestLister(headscale.NodeView{ID: "2", Hostname: "workpc"})

	runAutoBackfill(t, context.Background(), nil, hs, time.Hour)

	invalidations, lists, _, _ := hs.counts()
	if invalidations != 0 || lists != 0 {
		t.Errorf("with a nil DBSource the autoupdater must not query headscale (invalidations=%d lists=%d)", invalidations, lists)
	}
}

// nilCurrentDBSource is a DBSource that is NOT a nil interface but whose pool is
// missing — a typed-nil stored in the interface, or a wrapper around a pool that
// was never opened. `dbConn == nil` cannot see this shape.
type nilCurrentDBSource struct{}

func (nilCurrentDBSource) Current() *sql.DB { return nil }

// TestAutoBackfill_NilCurrentDBSourceIsSafe is the B325.1 regression guard for the
// finding that `AutoBackfill`'s "nil DB" check compared the DBSource INTERFACE with
// nil: this value walked straight past it, the loop started, and the first tick
// dereferenced the missing pool — inside a goroutine started from main, where a
// panic kills the whole process. The shared `db.DBCurrent` accessor answers nil for
// every shape, so both the starter and the tick body now refuse it.
func TestAutoBackfill_NilCurrentDBSourceIsSafe(t *testing.T) {
	hs := newAutoTestLister(headscale.NodeView{ID: "2", Hostname: "workpc"})

	runAutoBackfill(t, context.Background(), nilCurrentDBSource{}, hs, time.Hour)

	invalidations, lists, _, _ := hs.counts()
	if invalidations != 0 || lists != 0 {
		t.Errorf("with a DBSource whose Current() is nil the autoupdater must not query headscale (invalidations=%d lists=%d)", invalidations, lists)
	}

	// runOneTick has its own guard because it is also driven directly: it must
	// skip the tick, not panic on the nil pool.
	runOneTick(context.Background(), nilCurrentDBSource{}, hs, nil)
	if invalidations, lists, _, _ = hs.counts(); invalidations != 0 || lists != 0 {
		t.Errorf("runOneTick with a nil Current() must skip before touching headscale (invalidations=%d lists=%d)", invalidations, lists)
	}
}

// TestAutoBackfill_NilHeadscaleClientIsSafe: same guard for the headscale
// client (main.go also nil-checks HSGlobalFn, but the function must be safe on
// its own).
func TestAutoBackfill_NilHeadscaleClientIsSafe(t *testing.T) {
	src := newAutoTestDB(t)

	runAutoBackfill(t, context.Background(), src, nil, time.Hour)
	// Nothing to observe but "it returned" — a nil lister would panic on the
	// first hs.InvalidateCache() call.
}

// TestAutoBackfill_CancelledContextExitsWithoutTicking: the loop's ctx.Done
// branch. With an hour-long interval nothing can be ready but ctx.Done, so the
// function must return immediately and never call headscale.
func TestAutoBackfill_CancelledContextExitsWithoutTicking(t *testing.T) {
	src := newAutoTestDB(t)
	hs := newAutoTestLister()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runAutoBackfill(t, ctx, src, hs, time.Hour)

	if _, lists, _, _ := hs.counts(); lists != 0 {
		t.Errorf("ListAllNodes calls = %d, want 0 — a cancelled context must not run a tick", lists)
	}
}

// TestAutoBackfill_TickBackfillsAndReconciles is the end-to-end tick: a real
// migrated database holds the ownership row, headscale does NOT carry the tag,
// and one tick must repair the drift (B272's DATABASE → headscale direction).
//
// runOneTick is the loop body, so it is driven directly here: driving it through
// the ticker would race the cancellation (the loop's per-user ctx check returns
// before the reconcile pass) and make the assertions depend on scheduling
// rather than on the tick's behaviour. The ticker itself is covered by
// TestAutoBackfill_LoopTicksAndExitsOnCancel.
func TestAutoBackfill_TickBackfillsAndReconciles(t *testing.T) {
	// ReconcileTags cannot express a tag OWNER without the base domain, and
	// without an owner headscale refuses the tag ("invalid or not permitted").
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")

	src := newAutoTestDB(t)
	seedAutoTestUser(t, src.DB, "daniil", 7)
	seedAutoTestOwner(t, src.DB, "2", 7, "daniil", "tag:dev-daniil-workpc", "workpc")

	// UserID "77" belongs to no portal user, and the node has no preauth key /
	// tags / UserName, so Backfill's strategies (A/C/D/E) all decline — the tag
	// can only come from the reconciler, which is what this test isolates.
	hs := newAutoTestLister(headscale.NodeView{ID: "2", Hostname: "workpc", UserID: "77"})

	runOneTick(context.Background(), src, hs, nil)

	invalidations, lists, batches, ownerCalls := hs.counts()
	if invalidations == 0 || lists == 0 {
		t.Errorf("tick counts = invalidations=%d lists=%d, want at least one of each (InvalidateCache before every ListAllNodes is the fresh-data contract)", invalidations, lists)
	}
	// The reconciler applies exactly the row's tag. Backfill's strategies all
	// declined for this node (UserID 77, no preauth key, no tags, no UserName),
	// so nothing else can have written here.
	if got := hs.tagsFor(2); len(got) != 1 || got[0] != "tag:dev-daniil-workpc" {
		t.Errorf("tags applied to node 2 = %q, want exactly [tag:dev-daniil-workpc] — the tick did not reconcile the database row onto the node", got)
	}
	// The tag was permitted (B272.7 batch path), and by the RECONCILER, not the
	// per-user backfill (which calls the per-tag EnsureTagOwner instead).
	if batches != 1 {
		t.Errorf("EnsureTagOwners (batch) calls = %d, want 1 — the tick must permit pending tags in ONE policy write", batches)
	}
	if ownerCalls != 0 {
		t.Errorf("per-tag EnsureTagOwner calls = %d, want 0 — Backfill must not have matched this node (the reconciler did)", ownerCalls)
	}
	owners := hs.ownersOf("tag:dev-daniil-workpc")
	if len(owners) != 2 || owners[0] != "daniil@ts.example.com" || owners[1] != "tagged-devices@ts.example.com" {
		t.Errorf("tagOwners[tag:dev-daniil-workpc] = %v, want [daniil@ts.example.com tagged-devices@ts.example.com]", owners)
	}
	// The per-user pass must not have garbage-collected the live node's row.
	var rows int
	if err := src.DB.QueryRow(
		`SELECT COUNT(*) FROM node_owner_map WHERE node_id = '2' AND tag = 'tag:dev-daniil-workpc'`).Scan(&rows); err != nil {
		t.Fatalf("count node_owner_map: %v", err)
	}
	if rows != 1 {
		t.Errorf("node_owner_map rows for node 2 = %d, want 1 (the live node's row must survive the GC pass)", rows)
	}
}

// TestAutoBackfill_LoopTicksAndExitsOnCancel pins the loop contract: the ticker
// fires (ListAllNodes is reached) and a cancelled context ends the function — a
// leaked node-discovery goroutine would keep hitting the headscale API after
// shutdown.
func TestAutoBackfill_LoopTicksAndExitsOnCancel(t *testing.T) {
	src := newAutoTestDB(t)
	hs := newAutoTestLister()
	hs.tick = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		AutoBackfill(ctx, src, hs, nil, 10*time.Millisecond)
	}()

	select {
	case <-hs.tick:
	case <-time.After(time.Second):
		t.Fatal("no tick within 1s — AutoBackfill never reached ListAllNodes")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("AutoBackfill did not exit after the context was cancelled")
	}
	if _, lists, _, _ := hs.counts(); lists == 0 {
		t.Error("ListAllNodes was never called — the loop ticked without running the tick body")
	}
}

// TestAutoBackfill_TickSkipsWhenHeadscaleListFails: runOneTick is fire-and-
// forget by design — a headscale hiccup must skip the tick and let the next one
// try, without touching the database or panicking.
func TestAutoBackfill_TickSkipsWhenHeadscaleListFails(t *testing.T) {
	src := newAutoTestDB(t)
	seedAutoTestUser(t, src.DB, "daniil", 7)
	seedAutoTestOwner(t, src.DB, "2", 7, "daniil", "tag:dev-daniil-workpc", "workpc")

	hs := newAutoTestLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	hs.listErr = errors.New("api: headscale GET /api/v1/node: dial tcp 127.0.0.1:8081: connect: connection refused")

	runOneTick(context.Background(), src, hs, nil)

	_, lists, batches, ownerCalls := hs.counts()
	if lists != 1 {
		t.Errorf("ListAllNodes calls = %d, want 1", lists)
	}
	if batches != 0 || ownerCalls != 0 {
		t.Errorf("a failed node list must not write to the policy (batches=%d tagOwnerCalls=%d)", batches, ownerCalls)
	}
	if got := hs.tagsFor(2); len(got) != 0 {
		t.Errorf("AddTag was called (%q) even though the node list failed — the tick must be skipped", got)
	}
}
