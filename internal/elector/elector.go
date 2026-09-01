// Package elector — HA elector for the skygate cluster.
//
// v1.5.0+ / B204 — Phase 3.2 of
// docs/internal/cluster-management.md.
//
// Background
//
// The skygate-watchdog (B203) handles the *DB DSN* side
// of cluster health: it watches cluster_database and
// hot-reloads the pgxpool when the admin edits the row.
//
// The elector handles the *node liveness* side: it watches
// cluster_node.last_seen_at and transitions the state
// column based on heartbeat freshness:
//
//   pending → failed   last_seen is NULL and
//                       (NOW - joined_at) > 3*heartbeat_interval
//   ready   → failed   last_seen_at + 3*heartbeat_interval < NOW
//   failed  → ready    a fresh heartbeat arrived (handled by the
//                       Heartbeat() function in B201, NOT here)
//   ready   → ready    no-op
//
// Every transition is logged to cluster_audit (B195 schema)
// as action='node_health' with a JSONB detail payload
// capturing the from/to state, the last_seen_unix, the
// missed_seconds, and the reason. The /admin/cluster page
// surfaces these as a per-node "last health event" caption.
//
// Auto-failover
//
// In addition to state transitions, the elector runs an
// "auto-failover recommendation" pass on every tick:
// if a node with role=skygate is currently 'failed',
// AND there is at least one node with role=skygate-standby
// in state='ready', the elector logs a cluster_audit row
// with action='failover_recommend' naming the recommended
// target. It does NOT actually promote the standby — the
// promotion itself is an admin-gated action that lands
// in B205 (the cluster CLI subcommands). For now the
// recommendation surfaces in /admin/cluster as a banner
// "primary node failed, recommend promoting <standby>".
//
// Why a separate package (vs internal/cluster)
//
//   - No circular import (internal/cluster doesn't import
//     this package; this package imports internal/cluster
//     only for the constants).
//   - The elector's tick loop + state-machine logic is
//     testable in isolation (we stub the DB in
//     elector_b204_test.go).
//   - Future B205 work (CLI subcommands, manual promote)
//     can grow this package without bloating internal/cluster.
//
// Concurrency
//
// The elector runs as a single goroutine (one tick at a
// time). Each tick reads + updates the DB inside a
// transaction so the state transitions and the audit
// rows are atomic per node. The tick interval is
// tunable (default 5s, matches the skygate-watchdog
// default so the two tick in lockstep).

package elector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// HeartbeatIntervalSeconds is the recommended heartbeat
// interval exposed via the /api/cluster/heartbeat response.
// The elector uses it to compute the staleness threshold
// (3 * HeartbeatIntervalSeconds = 90s default).
//
// Why 30s: matches B201's HeartbeatHint. A longer interval
// would delay failure detection; a shorter one would
// generate more DB load. 30s is the canonical "30s
// heartbeat, 90s stale, 4× detection headroom" balance.
const HeartbeatIntervalSeconds = 30

// StaleMultiplier is how many heartbeat intervals a node
// may miss before being marked failed. 3 × 30s = 90s of
// silence. This is B204's "3 missed heartbeats → failed"
// contract.
const StaleMultiplier = 3

// Config is the elector's tunables.
type Config struct {
	// Interval between ticks. Default 5s (matches skygate-watchdog).
	Interval time.Duration

	// HeartbeatInterval is the expected gap between
	// heartbeats from a healthy node. Default 30s.
	// A node is marked failed if it has missed
	// StaleMultiplier * HeartbeatInterval seconds.
	HeartbeatInterval time.Duration

	// ClusterID is the cluster to elect within. Default
	// "skygate-staging" (cluster.DefaultClusterID).
	ClusterID string

	// Logger receives elector events. If nil, package-level
	// log.Printf is used.
	Logger func(format string, args ...any)
}

// DefaultConfig returns the recommended settings.
func DefaultConfig() Config {
	return Config{
		Interval:          5 * time.Second,
		HeartbeatInterval: HeartbeatIntervalSeconds * time.Second,
		ClusterID:         "skygate-staging",
		Logger:            log.Printf,
	}
}

// DBSource returns the current *sql.DB. Required because
// the elector's DB connection changes over time when the
// B203 skygate-watchdog hot-reloads the pool via the
// ResettableDB wrapper. Capturing a single *sql.DB at
// construction time would mean the elector keeps reading
// from a closed pool after the first hot-reload.
//
// The ResettableDB satisfies this interface directly
// via its Current() method. A plain *sql.DB also
// satisfies it (each call returns the same pointer).
type DBSource interface {
	Current() *sql.DB
}

// Elector is the running ticker. Construct with NewElector,
// then call Start to launch the goroutine.
type Elector struct {
	cfg     Config
	src     DBSource
	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
}

// NewElector constructs the elector. The DBSource is
// called on every tick to obtain the current *sql.DB
// (so B203 hot-reloads are followed transparently).
// If you only have a plain *sql.DB, NewElectorWithDB
// wraps it in a fixed-source adapter.
func NewElector(cfg Config, src DBSource) *Elector {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = HeartbeatIntervalSeconds * time.Second
	}
	if cfg.ClusterID == "" {
		cfg.ClusterID = "skygate-staging"
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Printf
	}
	return &Elector{
		cfg:  cfg,
		src:  src,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// NewElectorWithDB constructs an elector over a fixed
// *sql.DB (no hot-reload support). Useful for unit tests
// and one-off scripts; production code should pass the
// ResettableDB via NewElector.
func NewElectorWithDB(cfg Config, db *sql.DB) *Elector {
	return NewElector(cfg, fixedDB{db: db})
}

// fixedDB is a DBSource that always returns the same
// *sql.DB. The unit tests + one-off scripts use it.
type fixedDB struct {
	db *sql.DB
}

func (f fixedDB) Current() *sql.DB { return f.db }

// db is a convenience wrapper that returns the current
// *sql.DB (or nil if the source is nil / the pool is
// closed). The nil case is treated as a no-op by
// evaluate (logged + return).
func (e *Elector) db() *sql.DB {
	if e.src == nil {
		return nil
	}
	return e.src.Current()
}

// Start launches the elector goroutine. Returns immediately.
// Call Stop to terminate.
func (e *Elector) Start() {
	go e.run()
}

// Stop signals the goroutine to exit and waits for it.
// Safe to call multiple times (subsequent calls are no-ops).
func (e *Elector) Stop() {
	select {
	case <-e.stop:
		return
	default:
		close(e.stop)
	}
	<-e.done
}

// run is the main loop. One tick per cfg.Interval.
func (e *Elector) run() {
	defer close(e.done)
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.tick()
		}
	}
}

// tick is one iteration. Reads cluster_node, transitions
// states based on heartbeat freshness, logs to cluster_audit.
//
// Errors are logged at warn but do not stop the loop —
// a transient DB failure on one tick should not kill the
// elector. The next tick will retry.
func (e *Elector) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.evaluate(ctx); err != nil {
		e.cfg.Logger("elector: tick: %v", err)
	}
}

// evaluate is the testable inner loop. The exported tick
// wraps it with a context + error-logger; the unit tests
// call evaluate directly with a pre-canned DB.
func (e *Elector) evaluate(ctx context.Context) error {
	db := e.db()
	if db == nil {
		return fmt.Errorf("no current DB (source returned nil)")
	}
	now := time.Now().UTC()
	staleAfter := e.cfg.HeartbeatInterval * StaleMultiplier
	cutoff := now.Add(-staleAfter)

	// 1. Read all cluster_node rows for the cluster. The
	// sub-query picks only nodes that are NOT in the
	// terminal 'draining' state (the admin's "remove"
	// path; we don't auto-fail them).
	rows, err := db.QueryContext(ctx, `
		SELECT id, hostname, state, roles, last_seen_at, joined_at
		  FROM cluster_node
		 WHERE cluster_id = $1
		   AND state NOT IN ('draining')
		 ORDER BY hostname
	`, e.cfg.ClusterID)
	if err != nil {
		return fmt.Errorf("read cluster_node: %w", err)
	}
	defer rows.Close()

	var nodes []nodeRow
	for rows.Next() {
		var n nodeRow
		if err := rows.Scan(&n.ID, &n.Hostname, &n.State, &n.Roles, &n.LastSeen, &n.JoinedAt); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows: %w", err)
	}

	// 2. State transitions per node.
	for _, n := range nodes {
		newState, reason := nextState(n.State, n.LastSeen, n.JoinedAt, cutoff)
		if newState == n.State {
			continue
		}
		if err := e.transitionNode(ctx, n.ID, n.State, newState, n.LastSeen, reason, now); err != nil {
			e.cfg.Logger("elector: transition %s %s→%s: %v", n.Hostname, n.State, newState, err)
			continue
		}
		e.cfg.Logger("elector: %s: %s → %s (%s)", n.Hostname, n.State, newState, reason)
	}

	// 3. Auto-failover recommendation pass.
	e.recommendFailover(ctx, db, nodes, now)

	return nil
}

// nodeRow is the in-memory projection of one cluster_node
// row that the elector needs to make state-transition +
// failover-recommendation decisions. Roles is the raw
// TEXT[] literal as pgx v5 stdlib returns it (e.g.
// "{skygate,skygate-standby}"); roleContains() handles
// the parsing.
type nodeRow struct {
	ID       string
	Hostname string
	State    string
	Roles    string
	LastSeen sql.NullTime
	JoinedAt sql.NullTime
}

// nextState returns (newState, reason) for a node given
// its current state + last_seen + joined_at + the
// staleness cutoff. Returns (currentState, "") if no
// transition is needed.
//
// Rules:
//
//	pending + last_seen is NULL + joined_at < cutoff → failed (never heartbeated)
//	pending + last_seen     < cutoff               → failed (heartbeat stopped in pending)
//	ready   + last_seen     < cutoff               → failed (3+ missed heartbeats)
//	other                                      → no-op
//
// Note: a "pending" row that has a non-NULL last_seen is
// a valid state — the B200 admin AddNode helper sets
// last_seen_at = NOW() at insert time (so the row
// doesn't immediately get flagged as failed by the
// "no heartbeat since pending" check). The Heartbeat()
// call would normally transition pending → ready, but
// if heartbeats stop while still in pending, the node
// should be marked failed by the last_seen cutoff check
// (the same one that fires for "ready" rows).
func nextState(state string, lastSeen, joinedAt sql.NullTime, cutoff time.Time) (string, string) {
	switch state {
	case "pending":
		if !lastSeen.Valid && joinedAt.Valid && joinedAt.Time.Before(cutoff) {
			return "failed", "no heartbeat since pending (3× heartbeat interval)"
		}
		if lastSeen.Valid && lastSeen.Time.Before(cutoff) {
			return "failed", fmt.Sprintf("last_seen %s ago (3+ missed heartbeats)", time.Since(lastSeen.Time).Round(time.Second))
		}
		return state, ""
	case "ready":
		if lastSeen.Valid && lastSeen.Time.Before(cutoff) {
			return "failed", fmt.Sprintf("last_seen %s ago (3+ missed heartbeats)", time.Since(lastSeen.Time).Round(time.Second))
		}
		return state, ""
	}
	return state, ""
}

// transitionNode applies a state transition + writes the
// cluster_audit row in a single transaction. The from/to
// state are recorded in the JSONB detail so the admin UI
// can show "last health event" without a separate table.
func (e *Elector) transitionNode(
	ctx context.Context,
	nodeID, fromState, toState string,
	lastSeen sql.NullTime,
	reason string,
	now time.Time,
) error {
	db := e.db()
	if db == nil {
		return fmt.Errorf("no current DB (source returned nil)")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE cluster_node
		   SET state = $1
		 WHERE id = $2 AND state = $3
	`, toState, nodeID, fromState); err != nil {
		return fmt.Errorf("update cluster_node: %w", err)
	}

	detail := map[string]interface{}{
		"node_id":   nodeID,
		"from":      fromState,
		"to":        toState,
		"reason":    reason,
		"actor":     "elector",
		"timestamp": now.Unix(),
	}
	if lastSeen.Valid {
		detail["last_seen_unix"] = lastSeen.Time.Unix()
	}
	detailJSON, _ := json.Marshal(detail)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cluster_audit (
			cluster_id, action, target_node_id, detail, result
		) VALUES ($1, 'node_health', $2, $3::jsonb, 'ok')
	`, e.cfg.ClusterID, nodeID, string(detailJSON)); err != nil {
		return fmt.Errorf("insert cluster_audit: %w", err)
	}

	return tx.Commit()
}

// recommendFailover checks the cluster for a skygate
// primary that's currently in 'failed' state. If one
// exists AND at least one skygate-standby is 'ready',
// the elector logs a cluster_audit row with action
// 'failover_recommend' naming the recommended target.
// The actual promotion is admin-gated and lands in
// B205.
//
// Idempotent: only one recommend row is written per
// (failed_primary, target) pair. The next tick after
// the primary recovers (or the admin promotes manually)
// writes a new row.
func (e *Elector) recommendFailover(
	ctx context.Context,
	db *sql.DB,
	nodes []nodeRow,
	now time.Time,
) {
	var failedPrimary *nodeRow
	var readyStandbys []nodeRow
	for i := range nodes {
		n := &nodes[i]
		if !roleContains(n.Roles, "skygate") {
			continue
		}
		if n.State == "failed" {
			failedPrimary = n
		}
	}
	if failedPrimary == nil {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if n.State != "ready" {
			continue
		}
		if roleContains(n.Roles, "skygate-standby") {
			readyStandbys = append(readyStandbys, *n)
		}
	}
	if len(readyStandbys) == 0 {
		e.cfg.Logger("elector: primary %s is failed, but no ready skygate-standby to recommend (manual intervention required)", failedPrimary.Hostname)
		return
	}
	// Pick the lexicographically first ready standby.
	// (Deterministic so consecutive recommendations land
	// on the same target, and audit rows can be
	// deduplicated by the (from, to) pair.)
	target := readyStandbys[0]
	for _, s := range readyStandbys[1:] {
		if s.Hostname < target.Hostname {
			target = s
		}
	}

	// Idempotency: don't write a recommend row if one
	// already exists for the same (from, to) within the
	// last 5 minutes. This prevents a flood of audit
	// rows on every 5s tick while the primary stays
	// failed.
	var existing int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM cluster_audit
		 WHERE cluster_id = $1
		   AND action = 'failover_recommend'
		   AND detail->>'from_node_id' = $2
		   AND detail->>'to_node_id' = $3
		   AND created_at > NOW() - INTERVAL '5 minutes'
	`, e.cfg.ClusterID, failedPrimary.ID, target.ID).Scan(&existing)
	if err != nil {
		e.cfg.Logger("elector: dedup query: %v", err)
		return
	}
	if existing > 0 {
		return
	}

	detail := map[string]interface{}{
		"from_node_id":   failedPrimary.ID,
		"from_hostname":  failedPrimary.Hostname,
		"to_node_id":     target.ID,
		"to_hostname":    target.Hostname,
		"reason":         "primary node failed, ready standby available",
		"actor":          "elector",
		"timestamp":      now.Unix(),
		"recommended_at": now.Format(time.RFC3339),
	}
	detailJSON, _ := json.Marshal(detail)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO cluster_audit (
			cluster_id, action, target_node_id, detail, result
		) VALUES ($1, 'failover_recommend', $2, $3::jsonb, 'pending')
	`, e.cfg.ClusterID, target.ID, string(detailJSON)); err != nil {
		e.cfg.Logger("elector: insert failover_recommend: %v", err)
		return
	}
	e.cfg.Logger("elector: failover recommended: %s (failed) → %s (ready)", failedPrimary.Hostname, target.Hostname)
}

// roleContains reports whether the cluster_node.roles
// TEXT[] literal contains the given role string.
//
// The DB returns roles as "{skygate,skygate-standby}"
// (PG array literal). We avoid parsing it as a real
// array because the role list is short and the literal
// format is stable.
func roleContains(rolesLiteral, role string) bool {
	if rolesLiteral == "" {
		return false
	}
	// Strip braces.
	inner := rolesLiteral
	if len(inner) >= 2 && inner[0] == '{' && inner[len(inner)-1] == '}' {
		inner = inner[1 : len(inner)-1]
	}
	if inner == "" {
		return false
	}
	// Split on commas (no quoted-segment handling — role
	// names don't contain commas in our model).
	for _, p := range splitRolesLiteral(inner) {
		if p == role {
			return true
		}
	}
	return false
}

// splitRolesLiteral splits a PG array literal's inner
// content on commas, stripping surrounding double-quotes
// from each element.
func splitRolesLiteral(inner string) []string {
	var out []string
	start := 0
	for i := 0; i < len(inner); i++ {
		if inner[i] == ',' {
			out = append(out, stripRoleQuotes(inner[start:i]))
			start = i + 1
		}
	}
	out = append(out, stripRoleQuotes(inner[start:]))
	return out
}

func stripRoleQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
