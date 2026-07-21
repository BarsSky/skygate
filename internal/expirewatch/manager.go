// Package expirewatch — background watcher that keeps user-owned
// headscale nodes from being silently logged out.
//
// Why this exists
// ----------------
// The Tailscale 1.98.x clients (Linux, Android, iOS, macOS) send a
// RegisterRequest whose Expiry field is only a few seconds in the
// future. headscale 0.29.x's HandleNodeFromAuthPath (see
// hscontrol/state.go) applies that Expiry verbatim:
//
//	if !node.IsTagged() {
//	    if !regReq.Expiry.IsZero() {
//	        node.Expiry = &regReq.Expiry
//	    }
//	}
//
// The result is a non-tagged node with a 2-4 second expiry.
// Within a few seconds the next netmap push to the client
// reports `Expired: true, MachineAuthorized: false`, the
// Tailscale client interprets this as "your key was rejected,
// log out", and the device goes back to NeedsLogin. The
// original preauth key is already used (`used=true`), so
// re-registration is impossible. The user-facing symptom is
// "I generated a new key, the device registered, then
// immediately disconnected and won't come back".
//
// This was first observed on 2026-07-21 with the operator's
// Android phone (node id=10 / SkyBars): a fresh preauth (id=108)
// registered the node, but the 2-4s expiry dropped the device
// before the user could even see the connection.
//
// The v0.23.4 fix — "skip only nil-expiry nodes"
// ------------------------------------------------
// The original v0.23.3 release skipped any tagged node
// (`len(n.Tags) > 0`). That was wrong: a node can be tagged
// at *registration* and end up with nil Expiry (tag:exit-node,
// tag:public, tag:subnet-router). But a node that registers
// WITHOUT a tag — the common case for user devices — gets the
// 2-4s Expiry applied, and is *later* tagged by skygate's
// backfill (`tag:private` once the user logs in). The result:
// the node has both a tag AND an Expiry. v0.23.3's
// "tagged = skip" rule then froze the Expiry, the Expiry
// passed, the client disconnected.
//
// The corrected rule (v0.23.4) is simpler and correct:
//   - if n.Expiry == "" (nil), skip — there's nothing to
//     renew, and headscale's state.go never wrote one in
//     the first place. This covers tag:exit-node, tag:public,
//     tag:subnet-router, and any node on which the operator
//     ran `headscale nodes expire -i N --disable`.
//   - otherwise (Expiry present), apply the threshold check:
//     if Expiry is within Threshold (default 7d), renew it to
//     now + Renewal (default 30d); otherwise leave it alone.
//
// The tag set is no longer part of the skip rule. Tagged
// nodes with a real Expiry (e.g. skybars, skybars-1,
// Nothing Phone, Base — all `tag:private`) get renewed
// just like untagged ones.
//
// The watcher
// -----------
// Every TickInterval (default 5m), the goroutine lists every
// node in headscale and checks the Expiry field. For any
// node whose Expiry is present and within Threshold (default
// 7d), it calls headscale.ExtendNodeExpiry to push the
// expiry out to now + Renewal (default 30d).
//
// The audit_log table records every renewal:
//
//	expirewatch_renewed  detail="node_id=N expiry=2026-07-21T08:55:54Z -> 2026-08-20T12:00:00Z"
//
// so an operator can correlate "my device just reconnected"
// with "the watcher extended its expiry" via the admin audit
// page.
//
// Failure modes
// -------------
// If the gRPC API is unreachable (network blip, headscale
// restart, admin key rotation), ExtendNodeExpiry returns an
// error, the goroutine logs it, and the next tick tries
// again. There is no retry-with-backoff — the next tick is
// already at most TickInterval away, which is short enough
// that a missed renewal is recovered within a few minutes.
//
// If the operator sets SKYGATE_EXPIREWATCH_ENABLED=false the
// goroutine does not start at all; the same goes for
// SKYGATE_EXPIREWATCH_INTERVAL=off/0.
package expirewatch

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"skygate/internal/headscale"
)

// HSClient is the minimum surface of *headscale.Client the
// watcher needs. Defined as an interface so tests can inject
// a fake without spinning up a real headscale container.
type HSClient interface {
	ListAllNodes() ([]headscale.NodeView, error)
	ExtendNodeExpiry(nodeID int64, expiry time.Time) error
}

// AuditAppender is the minimum surface of db.AppendAuditLog
// the watcher needs. Same interface-seam rationale as HSClient.
type AuditAppender func(*sql.DB, int64, string, string, string) error

// Manager owns the watcher lifecycle. One per skygate process.
// Spawned by cmd/skygate/main.go on app start; Run() blocks
// the caller's goroutine until ctx is cancelled. Per the
// sidecar.Run convention (v0.16.7), the launch site MUST be
// `go manager.Run(ctx)` — calling Run synchronously blocks
// main() before the HTTP listener binds.
type Manager struct {
	DB       *sql.DB
	HS       HSClient
	Logger   *log.Logger
	Interval time.Duration

	// Threshold is the cutoff: a node whose Expiry is within
	// this window from "now" is considered "expiring soon"
	// and gets a renewal. Default 7d. Set to 0 to always
	// renew (useful for tests).
	Threshold time.Duration

	// Renewal is how far out to push Expiry when renewing.
	// Default 30d. Picked to outlast a typical operator
	// holiday without needing a watcher tick.
	Renewal time.Duration

	// AppendAudit is the db.AppendAuditLog wrapper. Bound at
	// construction so tests can stub it; production wires
	// db.AppendAuditLog directly.
	AppendAudit AuditAppender

	mu        sync.RWMutex
	lastTick  time.Time
	lastStats TickStats
}

// TickStats summarises one sync cycle. Exposed via
// Manager.LastStats() so the /admin/headscale page (or a
// future /admin/expirewatch page) can show "last tick: 3
// renewed, 0 errors" without touching headscale again.
type TickStats struct {
	At         time.Time
	NodesSeen  int
	Renewed    int
	Skipped    int
	Errors     int
	LastErrMsg string
}

// New returns a Manager with sensible defaults. The defaults
// match the env-var defaults in config.Load so a zero-config
// skygate (no SKYGATE_EXPIREWATCH_* env vars) still gets
// protection. If Interval <= 0 the goroutine will not start
// when Run is called — that's the disable path the env-var
// "off" / "0" check takes.
func New(db *sql.DB, hs HSClient, logger *log.Logger, interval time.Duration) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	return &Manager{
		DB:          db,
		HS:          hs,
		Logger:      logger,
		Interval:    interval,
		Threshold:   7 * 24 * time.Hour,
		Renewal:     30 * 24 * time.Hour,
		AppendAudit: nil, // set via SetAppendAudit before Run
	}
}

// SetAppendAudit binds the audit-log helper. Called from
// main.go right after construction so tests can leave it
// nil and the watcher no-ops on audit if not set.
func (m *Manager) SetAppendAudit(fn AuditAppender) {
	m.AppendAudit = fn
}

// Run is the main loop. Blocks until ctx is cancelled.
// Each tick runs SyncOnce. First tick is immediate (no
// warmup delay) so a freshly-restarted skygate fixes any
// already-expired nodes within the first few seconds
// rather than waiting one Interval.
func (m *Manager) Run(ctx context.Context) {
	if m.Interval <= 0 {
		m.Logger.Printf("expirewatch: disabled (interval <= 0). Expiry will not be auto-extended.")
		return
	}
	t := time.NewTicker(m.Interval)
	defer t.Stop()

	// First tick right away.
	if err := m.SyncOnce(ctx); err != nil {
		m.Logger.Printf("expirewatch.SyncOnce (initial): %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.SyncOnce(ctx); err != nil {
				m.Logger.Printf("expirewatch.SyncOnce: %v", err)
			}
		}
	}
}

// SyncOnce performs one full sweep. Steps:
//
//  1. List every node in headscale.
//  2. For each node, parse the Expiry field carried in
//     NodeView (added in v0.23.3 — previously this required
//     an extra /api/v1/node/{id} round trip per node per
//     tick).
//  3. If Expiry is missing, skip — nothing to renew
//     (covers tag:exit-node, tag:public, tag:subnet-router,
//     and operator-issued --disable).
//  4. If Expiry is within Threshold, call
//     ExtendNodeExpiry(now + Renewal) and append an audit
//     row.
//
// The tag set is intentionally NOT part of the skip rule
// (see package doc — v0.23.4 fix). Tagged nodes like
// tag:private (skybars, skybars-1, Nothing Phone, Base)
// carry a real Expiry from registration and are renewed
// just like untagged ones.
//
// Returns TickStats; also stores it on the Manager (under
// mu) for LastStats() to read.
func (m *Manager) SyncOnce(ctx context.Context) error {
	stats := TickStats{At: time.Now()}

	nodes, err := m.HS.ListAllNodes()
	if err != nil {
		stats.Errors++
		stats.LastErrMsg = err.Error()
		m.recordStats(stats)
		return fmt.Errorf("list nodes: %w", err)
	}
	stats.NodesSeen = len(nodes)

	now := time.Now()
	renewTo := now.Add(m.Renewal)
	cutoff := now.Add(m.Threshold)

	for _, n := range nodes {
		// Parse the node's current expiry. Headscale
		// returns "" for nil expiry, RFC3339Nano
		// otherwise (verified live on 2026-07-21:
		// "2026-07-21T10:34:46.386161411Z").
		expStr, expTime, hasExpiry := nodeExpiryFromCache(n)
		if !hasExpiry {
			// Expiry is intentionally nil (tagged at
			// registration, or operator ran --disable).
			// Nothing to renew; the watcher leaves
			// the node alone.
			stats.Skipped++
			continue
		}
		if expTime.After(cutoff) {
			// Expiry is comfortably in the future —
			// nothing to do.
			stats.Skipped++
			continue
		}
		// Expiring soon (or already expired). Renew.
		// The counters are updated AFTER the renew
		// succeeds so a mid-flight CLI failure shows
		// up as an error, not as a successful renewal.
		if err := m.renewOne(ctx, n, expStr, renewTo); err != nil {
			stats.Errors++
			stats.LastErrMsg = err.Error()
			m.Logger.Printf("expirewatch.renew node=%d (%s) old_expiry=%s new_expiry=%s: %v",
				nodeIDInt(n.ID), n.GivenName, expStr, renewTo.Format(time.RFC3339), err)
			continue
		}
		stats.Renewed++
	}

	m.recordStats(stats)
	m.Logger.Printf("expirewatch.tick: seen=%d renewed=%d skipped=%d errors=%d",
		stats.NodesSeen, stats.Renewed, stats.Skipped, stats.Errors)
	return nil
}

// renewOne extends a single node's expiry and appends an
// audit row. Splits the API call and the audit append so a
// failure in the audit helper doesn't mask a successful
// headscale write.
func (m *Manager) renewOne(ctx context.Context, n headscale.NodeView, oldExpiry string, newExpiry time.Time) error {
	nodeID := nodeIDInt(n.ID)
	if err := m.HS.ExtendNodeExpiry(nodeID, newExpiry); err != nil {
		return err
	}
	if m.AppendAudit != nil {
		detail := fmt.Sprintf("node_id=%d old_expiry=%s new_expiry=%s", nodeID, oldExpiry, newExpiry.Format(time.RFC3339))
		if err := m.AppendAudit(m.DB, 0, "expirewatch", "renewed", detail); err != nil {
			m.Logger.Printf("expirewatch.audit: node=%d: %v (renewal succeeded; audit failed)", nodeID, err)
		}
	}
	return nil
}

// isTagged was removed in v0.23.4. The old rule ("tagged
// = skip") froze the Expiry on nodes that were registered
// untagged (e.g. a fresh user device) and later tagged by
// skygate's backfill. The new rule is "skip only nil-expiry
// nodes" — see the package doc for the full story.

// nodeExpiryFromCache returns the current Expiry as a string
// (for audit) and as time.Time (for the cutoff comparison),
// plus a hasExpiry bool.
//
// The string is what headscale's /api/v1/node returns in
// the `expiry` field — RFC3339Nano with the trailing
// nanoseconds and the trailing Z. We try RFC3339Nano
// first, then RFC3339 (some 0.29.x patch versions drop
// the fractional second). On any parse error we treat the
// node as "no expiry" and the watcher renews it
// defensively (a wrongly-typed string is better than
// silent inaction when a client is about to be kicked out).
func nodeExpiryFromCache(n headscale.NodeView) (string, time.Time, bool) {
	raw := n.Expiry
	if raw == "" {
		return "", time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return raw, t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return raw, t, true
	}
	return raw, time.Time{}, false
}

// nodeIDInt parses the headscale node ID (string) back to
// int64. headscale's wire format is "10" (string), but
// ExtendNodeExpiry takes int64.
func nodeIDInt(s string) int64 {
	if s == "" {
		return 0
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (m *Manager) recordStats(s TickStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTick = s.At
	m.lastStats = s
}

// LastTick returns the wall-clock time of the most recent
// tick. Zero value if no tick has run yet.
func (m *Manager) LastTick() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastTick
}

// LastStats returns the most recent tick stats.
func (m *Manager) LastStats() TickStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastStats
}
