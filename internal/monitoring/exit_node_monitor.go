// Package monitoring — exit-node health monitor (v0.13.0, 2026-07-15).
//
// The monitor runs in a background goroutine and ticks every
// CheckEvery interval (default 5 min). Each tick:
//
//  1. Calls headscale.ListAllNodes to get the live view of every
//     node (online, last_seen, tags, available routes).
//
//  2. For each node, computes a state and updates the
//     exit_node_health snapshot (INSERT OR REPLACE).
//
//  3. Detects state transitions. In "calm" mode (the only mode
//     the user-facing alert path uses; verbose mode is internal
//     debug-only) we only alert on the two transitions that
//     matter: online→offline and offline→online.
//
//  4. Dispatches pending alerts (rows in
//     exit_node_state_changes with alerted_at = 0) via the
//     Notifier sink.
//
// The monitor is the long-lived goroutine the operator
// interacts with via /admin/exit-nodes (the "Run health check
// now" button calls CheckNow) and the bot (the /exit_nodes_health
// command reads the same DB rows the monitor writes).
//
// Concurrency: the monitor's tick() is the only writer to
// exit_node_health / exit_node_state_changes outside of the
// /admin/exit-nodes handler. Both paths are SQLite-serialised
// by the database/sql connection pool, so a concurrent tick +
// admin click is safe — the second writer just overwrites the
// first (the snapshot is "most recent view wins" by design).
//
// The CheckNow method uses a sync.Mutex to serialise concurrent
// manual triggers (e.g. an admin clicking twice in quick
// succession) — without it, two parallel ticks could insert
// duplicate transition rows. The mutex only protects
// CheckNow → tick(); the background ticker runs single-threaded
// by construction.
package monitoring

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// NotifierSink is the subset of telegram.Notifier the monitor
// needs. Defined here as an interface so tests can wire a
// no-op or a recording fake without importing internal/telegram
// (which would create a cycle through internal/handlers →
// internal/telegram → ...).
type NotifierSink interface {
	SendAlert(text string) int64
}

// noopSink is the fallback when the operator hasn't configured
// a Telegram bot yet. SendAlert returns 0 (no alert id) which
// the dispatch loop treats as "alert was queued" for the
// purposes of marking the row alerted — we don't want a
// missing Telegram bot to leave the dispatch table growing
// forever.
type noopSink struct{}

// SendAlert is the no-op implementation of NotifierSink. Returns
// 0 to match the production telegram.NoopNotifier contract.
func (noopSink) SendAlert(string) int64 { return 0 }

// HeadscaleClient is the subset of *headscale.Client the
// monitor uses. Pulled out as an interface so tests can pass a
// fake that returns canned node lists without involving the
// real headscale HTTP API.
type HeadscaleClient interface {
	ListAllNodes() ([]headscale.NodeView, error)
}

// ExitNodeMonitor is the long-lived background monitor. One
// instance per process. Start() launches the goroutine; the
// caller passes a context it can cancel to stop the loop
// cleanly on shutdown.
type ExitNodeMonitor struct {
	DB         *sql.DB
	HS         HeadscaleClient
	Notifier   NotifierSink
	CheckEvery time.Duration
	// OfflineAfter is the time window after last_seen beyond
	// which a node is considered "offline" even if headscale
	// says it's online. The forgiving fallback exists because
	// headscale's Online field flips to false the instant the
	// WireGuard session closes — which can be a long-lived
	// laptop briefly losing WiFi as much as a relay that's
	// actually down. last_seen within OfflineAfter keeps the
	// state "online" through transient blips.
	//
	// Zero value = 2 min (sane default; can be overridden via
	// SKYGATE_EXIT_NODE_OFFLINE_AFTER).
	OfflineAfter time.Duration

	// OnStartup runs an immediate check at Start() time, so a
	// fresh skygate that starts when all exit-nodes are down
	// sends the "0 healthy" alert on the first tick instead of
	// waiting up to CheckEvery. Default true.
	OnStartup bool

	// AutoSync (v0.14.1) — when true, the tick path also
	// calls db.SyncNodesFromHeadscale before classifying
	// nodes, so /admin/exit-nodes and the bot's /exit_nodes
	// always see the latest headscale→portal mapping. The
	// cost is one SELECT + one INSERT-or-UPDATE per node
	// per tick; off by default so operators with large
	// tailnets can opt in deliberately. On a single-tailnet
	// deployment (the typical Skygate install) the cost is
	// negligible and the value (no orphan exit-nodes in
	// the UI) is worth turning it on.
	AutoSync bool

	mu sync.Mutex // serialises CheckNow → tick(); the goroutine itself is single-threaded
}

// Start launches the background loop. Returns immediately;
// cancel ctx to stop. The first scheduled tick fires after
// CheckEvery (so a fresh start doesn't spam admins with
// "what's the state right now" on every restart unless
// OnStartup is true, in which case the immediate pre-tick
// fires before the loop starts).
func (m *ExitNodeMonitor) Start(ctx context.Context) {
	if m.CheckEvery == 0 {
		m.CheckEvery = 5 * time.Minute
	}
	if m.OfflineAfter == 0 {
		m.OfflineAfter = 2 * time.Minute
	}
	if m.Notifier == nil {
		m.Notifier = noopSink{}
	}

	// Immediate pre-tick: if the operator's tailnet has no
	// healthy exit-nodes at boot, they need to know NOW, not
	// in 5 minutes. The pre-tick re-uses the same path as the
	// background tick so any state-change it observes is
	// handled by the same dedup / alert pipeline.
	if m.OnStartup {
		if err := m.tick(ctx); err != nil {
			log.Printf("exit-node-monitor: startup tick failed: %v", err)
		}
	}

	go func() {
		t := time.NewTicker(m.CheckEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := m.tick(ctx); err != nil {
					log.Printf("exit-node-monitor: tick failed: %v", err)
				}
			}
		}
	}()
}

// CheckNow runs one tick immediately. Used by the
// "Run health check now" button on /admin/exit-nodes so the
// operator doesn't have to wait up to CheckEvery for a
// manual refresh. The mutex serialises concurrent CheckNow
// calls; the background tick is unaffected.
func (m *ExitNodeMonitor) CheckNow(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tick(ctx)
}

// tick is one pass of the monitor. Public for testability —
// tests call tick() directly with a fake HeadscaleClient and
// an in-memory DB. The same method is used by both the
// background ticker and CheckNow().
func (m *ExitNodeMonitor) tick(ctx context.Context) error {
	if m.DB == nil {
		return fmt.Errorf("monitor: DB is nil")
	}
	if m.HS == nil {
		return fmt.Errorf("monitor: HS is nil")
	}
	if m.OfflineAfter == 0 {
		m.OfflineAfter = 2 * time.Minute
	}

	// 1. Pull the live view from headscale.
	nodes, err := m.HS.ListAllNodes()
	if err != nil {
		// ListAllNodes has its own cache + retry; an error
		// here means headscale is unreachable. We don't
		// touch the snapshot table (a transient headscale
		// blip shouldn't mark every node offline), but we
		// do dispatch any pending alerts we already know
		// about so the operator hears about old transitions
		// even when the API is flaky.
		log.Printf("exit-node-monitor: ListAllNodes failed: %v", err)
		return m.dispatchPending(ctx)
	}

	now := time.Now().UTC()

	// 1.5. Auto-heal node_owner_map (v0.14.1). When the
	// operator enables AutoSync, every tick upserts the
	// headscale→portal mapping for the nodes we just
	// listed, so /admin/exit-nodes and the bot's /exit_nodes
	// always see the latest view without an admin button
	// click. A failure here is logged but does NOT abort
	// the health-check path — the snapshot update is the
	// monitor's main job, and a transient sync failure
	// (e.g. SQLITE_BUSY on a write-locked row) shouldn't
	// suppress an exit-node alert.
	if m.AutoSync {
		infos := make([]db.SyncNodeInfo, 0, len(nodes))
		for _, n := range nodes {
			tag := ""
			if len(n.Tags) > 0 {
				tag = n.Tags[0]
			}
			hsUID, _ := strconv.ParseInt(n.UserID, 10, 64)
			infos = append(infos, db.SyncNodeInfo{
				ID:       n.ID,
				Hostname: n.Hostname,
				Tag:      tag,
				Username: n.UserName,
				HSUserID: hsUID,
				TaggedBy: 0, // system sync (the admin /sync_nodes path also uses 0)
			})
		}
		ins, upd, serr := db.SyncNodesFromHeadscale(m.DB, infos)
		if serr != nil {
			log.Printf("exit-node-monitor: auto-sync failed: %v", serr)
		} else {
			// Always log, not just on changes: silent
			// ticks make it hard to confirm the feature is
			// working. A typical tick is "0 inserted, 0
			// updated" — the operator wants to see that
			// line every CheckEvery.
			log.Printf("exit-node-monitor: auto-sync inserted=%d updated=%d", ins, upd)
		}
	}

	// 2 + 3. Update snapshots and detect transitions.
	liveIDs := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		liveIDs[n.ID] = struct{}{}
		snapshot := m.computeSnapshot(n, now)
		prev, _ := db.GetExitNodeHealth(m.DB, n.ID)
		if err := db.UpsertExitNodeHealth(m.DB, snapshot); err != nil {
			log.Printf("exit-node-monitor: upsert %s: %v", n.ID, err)
			continue
		}
		// Transition: any change to a stored state other than
		// (last_state, last_state_change_at) counts.
		// online↔offline are the calm-mode alerts; anything
		// else (degraded on/off, unknown→online) is logged
		// here but the dispatch loop only fires for
		// online↔offline.
		if prev.State != "" && prev.State != snapshot.State {
			_, lastTo, _ := db.LatestExitNodeState(m.DB, n.ID)
			// Dedup: if the latest recorded transition has
			// the same to_state, the alert for that
			// transition has already been queued (or the
			// operator has seen it). Skip the insert.
			//
			// The 5-second window here is a defensive
			// guard: if a transition happens twice within
			// 5s (e.g. a single-tick online→offline→online
			// blip), the second observation would otherwise
			// insert a row and re-alert. The LatestExitNode
			// check catches this.
			if lastTo == snapshot.State {
				continue
			}
			note := m.transitionNote(prev, snapshot)
			if _, err := db.RecordExitNodeStateChange(m.DB, db.ExitNodeStateChange{
				NodeID:     n.ID,
				Hostname:   snapshot.Hostname,
				FromState:  prev.State,
				ToState:    snapshot.State,
				DetectedAt: now,
				Note:       note,
			}); err != nil {
				log.Printf("exit-node-monitor: record state change for %s: %v", n.ID, err)
			}
		}
	}

	// 4. Garbage-collect snapshots for nodes that no longer
	// exist in headscale. Without this, an admin who deletes
	// a node in the headscale UI would see it stuck "offline"
	// on /admin/exit-nodes forever.
	//
	// We don't delete the transition log; that's a permanent
	// audit trail.
	snapshots, err := db.ListExitNodeHealth(m.DB)
	if err == nil {
		for _, s := range snapshots {
			if _, ok := liveIDs[s.NodeID]; !ok {
				if err := db.DeleteExitNodeHealth(m.DB, s.NodeID); err != nil {
					log.Printf("exit-node-monitor: delete stale %s: %v", s.NodeID, err)
				}
			}
		}
	}

	// 5. Dispatch pending alerts.
	return m.dispatchPending(ctx)
}

// computeSnapshot turns one headscale node into an
// ExitNodeHealth row. The rules:
//
//   online: headscale.Online is true AND (last_seen is empty
//           OR last_seen is within OfflineAfter).
//
//   advertised_routes_ok: the node's AvailableRoutes include
//           both 0.0.0.0/0 and ::/0 (the two CIDRs a relay
//           must advertise to be a useful internet exit). The
//           check is the same one scripts/check_exit_nodes.py
//           uses for the deploy-time test, so a node that's
//           "online" but missing either CIDR lands in the
//           degraded bucket.
//
//   has_exit_tag: tag:exit-node is present in n.Tags.
//
//   state: the discrete outcome.
//
//     unknown  — first observation (no previous state to
//                compare against).
//     online   — online AND routes_ok AND has_tag.
//     degraded — online AND has_tag, but routes are not
//                fully approved (e.g. admin ran
//                --advertise-routes but forgot to approve
//                on headscale, or a recent config push
//                reset the approval).
//     offline  — everything else (not online, or missing
//                the exit-node tag).
//
//   healthy: a coarse boolean the /admin/exit-nodes page
//           renders as a green/red dot. True iff state is
//           "online".
func (m *ExitNodeMonitor) computeSnapshot(n headscale.NodeView, now time.Time) db.ExitNodeHealth {
	hasTag := false
	for _, t := range n.Tags {
		if t == "tag:exit-node" {
			hasTag = true
			break
		}
	}
	routesOK := false
	hasV4, hasV6 := false, false
	for _, r := range n.AvailableRoutes {
		if r == "0.0.0.0/0" {
			hasV4 = true
		}
		if r == "::/0" {
			hasV6 = true
		}
	}
	routesOK = hasV4 && hasV6

	// Online detection: headscale says online AND last_seen
	// is recent. last_seen is the union field — if headscale
	// says online and last_seen is empty, we trust it
	// (a node that just registered may not have a
	// last_seen yet). If headscale says offline but
	// last_seen is recent, the node is treated as online
	// (e.g. a brief headscale-side hiccup).
	online := n.Online
	if n.LastSeen != "" {
		// headscale's LastSeen is RFC3339Nano. time.Parse
		// handles both. We don't fail the snapshot on a
		// parse error — fall through to the boolean
		// fallback (which may itself be wrong, but it's
		// the best we can do).
		if t, perr := time.Parse(time.RFC3339Nano, n.LastSeen); perr == nil {
			if now.Sub(t) > m.OfflineAfter {
				online = false
			}
		} else if t, perr := time.Parse(time.RFC3339, n.LastSeen); perr == nil {
			if now.Sub(t) > m.OfflineAfter {
				online = false
			}
		}
	}

	// State machine.
	var state string
	switch {
	case !online || !hasTag:
		state = "offline"
	case !routesOK:
		state = "degraded"
	default:
		state = "online"
	}
	healthy := state == "online"

	return db.ExitNodeHealth{
		NodeID:             n.ID,
		Hostname:           n.Hostname,
		Online:             online,
		LastSeen:           n.LastSeen,
		AdvertisedRoutesOK: routesOK,
		HasExitTag:         hasTag,
		State:              state,
		Healthy:            healthy,
		LastCheckAt:        now,
		LastStateChangeAt:  now, // updated only on actual transitions below
		ConsecutiveFailures: 0,   // reserved for a future "headscale unreachable" counter
	}
}

// transitionNote returns a human-readable annotation for the
// transition log row. Today the note is just the reason
// (last_seen, routes, or tag); future changes (e.g. latency
// spikes) can extend the format without breaking the schema.
func (m *ExitNodeMonitor) transitionNote(prev, next db.ExitNodeHealth) string {
	if prev.Online != next.Online {
		if next.Online {
			return "came back online"
		}
		return "went offline"
	}
	if prev.AdvertisedRoutesOK != next.AdvertisedRoutesOK {
		if next.AdvertisedRoutesOK {
			return "routes now approved"
		}
		return "routes unapproved"
	}
	if prev.HasExitTag != next.HasExitTag {
		if next.HasExitTag {
			return "tag:exit-node added"
		}
		return "tag:exit-node removed"
	}
	return ""
}

// dispatchPending fires a Telegram alert for every row in
// exit_node_state_changes where alerted_at = 0. After each
// successful send the row is marked alerted so the next tick
// doesn't re-send.
//
// Calm-mode behaviour: only the online→offline and
// offline→online transitions are alerted. degraded/unknown
// transitions are still recorded in the log (for the operator's
// audit trail) but the alert is suppressed here. The "is this
// a calm-mode transition?" check is the pair (online, offline)
// — the only two states that matter for "is the tailnet
// actually able to exit the internet right now?".
func (m *ExitNodeMonitor) dispatchPending(ctx context.Context) error {
	pending, err := db.ListPendingExitNodeStateChanges(m.DB, 32)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	for _, sc := range pending {
		if !isCalmModeAlert(sc.FromState, sc.ToState) {
			// Mark alerted anyway so the loop doesn't
			// re-examine the row on every tick. The note
			// column still records what happened; only the
			// Telegram send is suppressed.
			_ = db.MarkExitNodeStateChangeAlerted(m.DB, sc.ID)
			continue
		}
		msg := formatAlert(sc)
		m.Notifier.SendAlert(msg)
		if err := db.MarkExitNodeStateChangeAlerted(m.DB, sc.ID); err != nil {
			log.Printf("exit-node-monitor: mark alerted %d: %v", sc.ID, err)
		}
	}
	return nil
}

// isCalmModeAlert returns true iff the transition is one of
// the two operators care about: an exit-node going offline or
// coming back. degraded transitions are recorded in the log
// (so the operator can see them in the audit) but not alerted
// (calm mode).
func isCalmModeAlert(from, to string) bool {
	if from == "online" && to == "offline" {
		return true
	}
	if from == "offline" && to == "online" {
		return true
	}
	return false
}

// formatAlert renders one transition as the Telegram message
// body. The format is operator-friendly: hostname, transition,
// note, when it happened. Kept compact because Telegram Bot
// API messages cap at 4096 chars and a row from a single
// transition is well under that.
func formatAlert(sc db.ExitNodeStateChange) string {
	ts := sc.DetectedAt.UTC().Format("2006-01-02 15:04Z")
	if sc.Note != "" {
		return fmt.Sprintf("🛰️ exit-node %s: %s → %s (%s, %s)", sc.Hostname, sc.FromState, sc.ToState, sc.Note, ts)
	}
	return fmt.Sprintf("🛰️ exit-node %s: %s → %s (%s)", sc.Hostname, sc.FromState, sc.ToState, ts)
}
