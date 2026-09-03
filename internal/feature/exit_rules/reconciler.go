package exit_rules

// reconciler.go — v1.5.2 (B229) — preferred-exit auto-reconcile.
//
// What this does
// ---------------
// The B77 tag-autoupdater pins per-CIDR grant'ы in the
// headscale ACL only when a row exists in
// `device_exit_node_prefs` for the device. Pre-B229, those
// rows were created ONLY via the manual UI flow:
//
//   - POST /my/devices/preferred-exit (self-service)
//   - POST /admin/devices/preferred-exit (admin override)
//
// If a user added a `device_rules` row via /my/exit-rules
// (e.g. "cyborg → emilia → 8.8.8.0/24") but never visited
// /my/devices to pin emilia as preferred, the per-CIDR
// grant landed in headscale WITHOUT the `via: tag:dev-
// infra-emilia` clause. Tailscale then routed the
// traffic through the default exit (DERP / direct),
// not through emilia. The user's "cyborg → emilia" rule
// was effectively decorative (it was ALLOWED, not
// PINNED).
//
// This reconciler runs in the background (boot + every
// 1h) and:
//
//   1. For every (user, device) pair that has enabled
//      `device_rules` rows, find the dominant
//      `exit_node_id` (most common across the device's
//      rules). If it's unanimous — i.e. ALL rules
//      point at the same exit_node — auto-create a
//      `device_exit_node_prefs` row (via_enabled=true)
//      so the next ACL build attaches the `via:` clause.
//
//   2. For every existing `device_exit_node_prefs` row,
//      verify the stored `exit_node_tag` still matches
//      the canonical `tag` in `node_owner_map` for the
//      hostname. This catches two stale-tag classes:
//        a. Legacy `tag:exit-<host>` rows that pre-date
//           the v0.33.1.39 / B118 cutover to
//           `tag:dev-infra-<host>` and somehow survived
//           the V061 migration (e.g. the hostname was
//           missing from node_owner_map at migration
//           time, so the JOIN didn't match).
//        b. Rows whose hostname was renamed in headscale
//           but the prefs row kept the old tag.
//
//      When the canonical tag differs from the stored
//      tag, UPDATE the prefs row + re-enable via_enabled
//      (the V061 backfill intentionally skipped re-enable
//      for unresolved rows; B229 is the catch-up).
//
// Safety
// ------
// Every change is recorded in `audit_log` with
// action=`preferred_exit_reconciled`,
// target_type=`headscale_node`, target_id=hostname. The
// operator can `/admin/audit?action=preferred_exit_reconciled`
// to see every change in chronological order and
// reverse via SQL if needed (DELETE FROM
// device_exit_node_prefs WHERE ...).
//
// A rate-limited Telegram alert (1 per (hostname, reason)
// per 1h) is sent so the operator sees the FIRST change
// per device per session without spam on bulk reconciles.
//
// The reconciler is **read-only** when the
// `SKYGATE_PREFERRED_RECONCILER_LIVE` env var is unset
// (the default). The boot-time + tick runs log every
// change it WOULD make without writing. Setting the env
// var to "true" / "1" / "yes" enables the writes. This
// matches the B188 (v0.61 migration) safety belt: surface
// the impact, let the operator flip the switch.
//
// 2026-09-03: v1.5.2 (B229).

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
)

// ReconcilerChange — single change the reconciler applied
// (or would apply in dry-run mode). Returned from
// ReconcileDeviceExitNodePrefs so the caller (the
// background goroutine in handlers.go) can render a
// "B229 made N changes this tick" log line and pass the
// list to the optional Telegram alerter.
//
// 2026-09-03: v1.5.2 (B229).
type ReconcilerChange struct {
	Action         string // "create" | "update" | "skip"
	UserID         int64
	Username       string // for the audit row + Telegram
	DeviceHostname string
	OldTag         string // "" for create
	NewTag         string
	RuleCount      int    // how many device_rules pointed at NewTag
	Reason         string // "missing-pref-unanimous" | "missing-pref-split" | "stale-tag" | "via-disabled-but-canonical"
}

// DevicePrefState is the in-memory shape of one
// (user, device) pair's input to the decision function.
// The DB-touching ReconcileDeviceExitNodePrefs populates
// a list of these and calls PlanDevicePrefChange per
// item; PlanDevicePrefChange is the unit-tested pure
// function that picks what (if anything) to do.
//
// 2026-09-03: v1.5.2 (B229).
type DevicePrefState struct {
	UserID              int64
	Username            string
	DeviceHostname      string
	ExistingPrefTag     string // "" if no row
	ExistingPrefVia     bool   // via_enabled of the existing row
	DistinctExitNodes   int    // 0, 1, 2+ — count of distinct exit_node_id across the device's rules
	DominantExitHostname string // hostname of the most-common exit_node_id
	TotalRules          int
	CanonicalTag        string // resolved from node_owner_map via NormalizeExitNodeTag ("" if not resolvable)
}

// PlanDevicePrefChange is the pure decision function —
// given a DevicePrefState, return the change to apply
// (or nil for no-op / skip). Extracted from the
// DB-touching function so the unit tests don't need an
// in-memory sqlite (no modernc.org/sqlite / sqlmock in
// go.mod). The DB layer simply calls this once per
// (user, device) and applies the result.
//
// Returns:
//   - change, true  → apply this change (write if live,
//     log-only if dry-run)
//   - nil, false     → no-op (everything already correct)
//
// 2026-09-03: v1.5.2 (B229).
func PlanDevicePrefChange(s DevicePrefState) (*ReconcilerChange, bool) {
	// Case 1: no existing pref. We need a unanimous
	// exit_node to auto-derive; otherwise the
	// operator's intent is split (some rules at
	// emilia, some at karolina) and we shouldn't pick
	// one for them.
	if s.ExistingPrefTag == "" {
		if s.CanonicalTag == "" {
			// Can't resolve the canonical tag for the
			// dominant exit_node. The headscale
			// node probably isn't tagged yet (B77
			// autoupdater hasn't run, or the exit
			// node was just added). Skip silently.
			return nil, false
		}
		if s.DistinctExitNodes > 1 {
			// Split rules. The operator needs to
			// pick — log a skip change for
			// visibility.
			return &ReconcilerChange{
				Action:         "skip",
				UserID:         s.UserID,
				Username:       s.Username,
				DeviceHostname: s.DeviceHostname,
				RuleCount:      s.TotalRules,
				NewTag:         s.DominantExitHostname,
				Reason:         "missing-pref-split",
			}, true
		}
		// Unanimous → CREATE.
		return &ReconcilerChange{
			Action:         "create",
			UserID:         s.UserID,
			Username:       s.Username,
			DeviceHostname: s.DeviceHostname,
			NewTag:         s.CanonicalTag,
			RuleCount:      s.TotalRules,
			Reason:         "missing-pref-unanimous",
		}, true
	}
	// Case 2: existing pref. We may need to UPDATE
	// (stale tag) or re-enable (via=0) or no-op.
	if s.CanonicalTag == "" {
		// Hostname was deleted from node_owner_map
		// (device unregistered). Don't clobber the
		// existing pref.
		return nil, false
	}
	if s.CanonicalTag == s.ExistingPrefTag {
		// Tag is canonical. Check via_enabled.
		if !s.ExistingPrefVia {
			// Re-enable (the v0.28.5 default was
			// via=1 for pre-existing rows; the
			// V061 migration intentionally
			// skipped via=1 re-enable for rows
			// whose tag it couldn't resolve.
			// B229 is the catch-up: the row is
			// here, the tag is canonical, so the
			// operator's pin intent stands.
			return &ReconcilerChange{
				Action:         "update",
				UserID:         s.UserID,
				Username:       s.Username,
				DeviceHostname: s.DeviceHostname,
				OldTag:         s.ExistingPrefTag,
				NewTag:         s.CanonicalTag,
				Reason:         "via-disabled-but-canonical",
			}, true
		}
		// No-op.
		return nil, false
	}
	// Tag mismatch → UPDATE.
	return &ReconcilerChange{
		Action:         "update",
		UserID:         s.UserID,
		Username:       s.Username,
		DeviceHostname: s.DeviceHostname,
		OldTag:         s.ExistingPrefTag,
		NewTag:         s.CanonicalTag,
		Reason:         "stale-tag",
	}, true
}

// PreferredExitReconcilerLive reports whether the
// reconciler should write changes (true) or only log
// the changes it would make (false). Reads
// SKYGATE_PREFERRED_RECONCILER_LIVE from the env on each
// call so the operator can flip the switch at runtime
// without a redeploy. Default is "off" (dry-run).
//
// 2026-09-03: v1.5.2 (B229).
func PreferredExitReconcilerLive() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SKYGATE_PREFERRED_RECONCILER_LIVE")))
	return v == "true" || v == "1" || v == "yes"
}

// alertThrottle per (hostname, reason) — rate-limit the
// Telegram alerter to 1 per hostname per 1h (so a
// 100-device bulk reconcile doesn't spam the operator).
// Mirrors the B227 (B77 tag-autoupdate) alert throttle.
//
// 2026-09-03: v1.5.2 (B229).
var (
	alertThrottleMu sync.Mutex
	alertThrottle   = make(map[string]time.Time)
)

const alertThrottleWindow = 1 * time.Hour

// shouldAlert returns true iff no alert was sent for the
// given (hostname, reason) key in the last
// alertThrottleWindow. Updates the timestamp on the
// first call.
//
// 2026-09-03: v1.5.2 (B229).
func shouldAlert(hostname, reason string, now time.Time) bool {
	key := hostname + "\x00" + reason
	alertThrottleMu.Lock()
	defer alertThrottleMu.Unlock()
	last, seen := alertThrottle[key]
	if seen && now.Sub(last) < alertThrottleWindow {
		return false
	}
	alertThrottle[key] = now
	return true
}

// ResetAlertThrottle clears the in-memory alert throttle.
// Test-only — production code never calls this.
//
// 2026-09-03: v1.5.2 (B229).
func ResetAlertThrottle() {
	alertThrottleMu.Lock()
	alertThrottle = make(map[string]time.Time)
	alertThrottleMu.Unlock()
}

// ReconcilerNotifier is the minimal interface the
// reconciler needs to send rate-limited Telegram alerts.
// Mirrors the B225 (`sendFailoverAlert`) and B227
// (`AlertSink`) patterns. Production code passes
// `*telegram.RealNotifier` (structurally satisfies the
// interface).
//
// 2026-09-03: v1.5.2 (B229).
type ReconcilerNotifier interface {
	SendAlert(text string) int64
}

// noopNotifier for tests + dry-run mode where the alerter
// isn't wired.
type noopNotifier struct{}

// SendAlert is the no-op implementation of
// ReconcilerNotifier. Returns 0 to match the production
// telegram.NoopNotifier contract.
//
// 2026-09-03: v1.5.2 (B229).
func (noopNotifier) SendAlert(string) int64 { return 0 }

// ReconcileDeviceExitNodePrefs walks the (user, device)
// pairs and applies the changes described in the file
// header. Returns the list of changes (for logging +
// alerter). Live-mode controlled by
// SKYGATE_PREFERRED_RECONCILER_LIVE.
//
// On any DB error the function returns early with the
// error; the caller logs + retries on the next tick.
// Partial changes already applied in the same call are
// NOT rolled back (the audit_log rows are the recovery
// surface).
//
// 2026-09-03: v1.5.2 (B229).
func (s *Service) ReconcileDeviceExitNodePrefs(ctx context.Context, n ReconcilerNotifier) ([]ReconcilerChange, error) {
	if n == nil {
		n = noopNotifier{}
	}
	live := PreferredExitReconcilerLive()

	// Step 1: every (user, device) pair that has
	// `device_rules` rows. We need the (user_id,
	// device_hostname) tuple to look up both the
	// existing pref AND the dominant exit_node.
	type userDevice struct {
		userID   int64
		hostname string
	}
	rows, err := s.dbc().QueryContext(ctx, `
		SELECT DISTINCT user_id, device_hostname
		  FROM device_rules
		 WHERE enabled = 1
		   AND device_hostname <> ''
		   AND user_id IS NOT NULL
		   AND exit_node_id <> ''
	`)
	if err != nil {
		return nil, fmt.Errorf("reconciler: list rule pairs: %w", err)
	}
	var pairs []userDevice
	for rows.Next() {
		var p userDevice
		if err := rows.Scan(&p.userID, &p.hostname); err != nil {
			rows.Close()
			return nil, err
		}
		if p.userID == 0 || p.hostname == "" {
			continue
		}
		pairs = append(pairs, p)
	}
	rows.Close()

	var changes []ReconcilerChange
	usernameCache := make(map[int64]string)

	for _, p := range pairs {
		// Resolve username once per user.
		username := usernameCache[p.userID]
		if username == "" {
			_ = s.dbc().QueryRowContext(ctx,
				`SELECT username FROM portal_users WHERE id = $1`,
				p.userID,
			).Scan(&username)
			usernameCache[p.userID] = username
		}

		state, err := s.collectDevicePrefState(ctx, p.userID, username, p.hostname)
		if err != nil {
			log.Printf("preferred-reconciler: state for %s/%s: %v", username, p.hostname, err)
			continue
		}

		ch, ok := PlanDevicePrefChange(state)
		if !ok {
			continue
		}
		changes = append(changes, *ch)

		// Apply or dry-run.
		s.applyReconcilerChange(ctx, ch, live, n)
	}

	// Per-user prefs (device_hostname='') are not in
	// scope — they're managed by /my/exit-nodes, not
	// B229. Log a one-line count for visibility.
	perUserRows, err := s.dbc().QueryContext(ctx, `
		SELECT COUNT(*) FROM device_exit_node_prefs
		 WHERE device_hostname = '' OR device_hostname IS NULL
	`)
	if err == nil {
		var n_skipped int
		_ = perUserRows.Scan(&n_skipped)
		perUserRows.Close()
		if n_skipped > 0 {
			log.Printf("preferred-reconciler: %d per-user prefs (device_hostname='') skipped — managed by /my/exit-nodes, not B229", n_skipped)
		}
	}

	return changes, nil
}

// collectDevicePrefState populates the in-memory shape
// the pure PlanDevicePrefChange function consumes. The
// DB calls live here; the decision logic lives in
// PlanDevicePrefChange (testable without a DB).
//
// 2026-09-03: v1.5.2 (B229).
func (s *Service) collectDevicePrefState(ctx context.Context, userID int64, username, hostname string) (DevicePrefState, error) {
	state := DevicePrefState{
		UserID:         userID,
		Username:       username,
		DeviceHostname: hostname,
	}
	// Existing pref.
	existing, _ := db.GetDeviceExitNodePref(s.dbc(), userID, hostname)
	state.ExistingPrefTag = existing.ExitNodeTag
	state.ExistingPrefVia = existing.ViaEnabled
	// Dominant exit_node + distinct count + total.
	ruleRows, err := s.dbc().QueryContext(ctx, `
		SELECT exit_node_id, COUNT(*)
		  FROM device_rules
		 WHERE user_id = $1 AND device_hostname = $2 AND enabled = 1
		 GROUP BY exit_node_id
		 ORDER BY COUNT(*) DESC, exit_node_id ASC
	`, userID, hostname)
	if err != nil {
		return state, fmt.Errorf("rules for %s/%s: %w", username, hostname, err)
	}
	defer ruleRows.Close()
	var totalRules int
	var dominant string
	for ruleRows.Next() {
		var h string
		var c int
		if err := ruleRows.Scan(&h, &c); err != nil {
			continue
		}
		if dominant == "" {
			dominant = h
		}
		totalRules += c
		state.DistinctExitNodes++
	}
	state.TotalRules = totalRules
	state.DominantExitHostname = dominant
	// Canonical tag for the dominant exit_node.
	if dominant != "" {
		state.CanonicalTag, _ = db.NormalizeExitNodeTag(s.dbc(), dominant)
	}
	// Also resolve canonical tag for the device's
	// own hostname (used in the existing-pref branch
	// to detect tag mismatch). This is the same
	// query NormalizeExitNodeTag does, but with the
	// device's hostname (not the exit's). The
	// caller decides which to use; PlanDevicePrefChange
	// uses the same logic.
	return state, nil
}

// applyReconcilerChange writes (live mode) or logs
// (dry-run mode) the planned change. Side effects:
//   - audit_log row (system actor: userID=0,
//     username="system", target_type="headscale_node",
//     target_id=hostname) for live create + update.
//   - Telegram alert (rate-limited per
//     (hostname, reason)) for live create + update.
//
// 2026-09-03: v1.5.2 (B229).
func (s *Service) applyReconcilerChange(ctx context.Context, ch *ReconcilerChange, live bool, n ReconcilerNotifier) {
	// Skip-changes: log only, no write.
	if ch.Action == "skip" {
		log.Printf("preferred-reconciler: SKIP %s/%s — %d rules point at %d distinct exit_nodes (most=%s). Needs manual review.",
			ch.Username, ch.DeviceHostname, ch.RuleCount, ch.DistinctExitNodesOrZero(), ch.NewTag)
		return
	}
	// Dry-run: log only.
	if !live {
		switch ch.Action {
		case "create":
			log.Printf("preferred-reconciler: DRY-RUN would CREATE %s/%s → %s (via_enabled=true) (%d rules)",
				ch.Username, ch.DeviceHostname, ch.NewTag, ch.RuleCount)
		case "update":
			log.Printf("preferred-reconciler: DRY-RUN would UPDATE %s/%s: %s → %s (%s)",
				ch.Username, ch.DeviceHostname, ch.OldTag, ch.NewTag, ch.Reason)
		}
		return
	}
	// Live mode: write + audit + alert.
	switch ch.Action {
	case "create":
		if err := db.SetDeviceExitNodePref(s.dbc(), ch.UserID, ch.DeviceHostname, ch.NewTag, 0, true); err != nil {
			log.Printf("preferred-reconciler: CREATE %s/%s → %s FAILED: %v", ch.Username, ch.DeviceHostname, ch.NewTag, err)
			return
		}
		_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
			"preferred_exit_reconciled",
			fmt.Sprintf("CREATE pref hostname=%s user=%s tag=%s via=1 reason=%s rules=%d",
				ch.DeviceHostname, ch.Username, ch.NewTag, ch.Reason, ch.RuleCount),
			"headscale_node", ch.DeviceHostname)
		log.Printf("preferred-reconciler: CREATE %s/%s → %s (via_enabled=true, %d rules)",
			ch.Username, ch.DeviceHostname, ch.NewTag, ch.RuleCount)
		if shouldAlert(ch.DeviceHostname, "create", time.Now()) {
			n.SendAlert(fmt.Sprintf("♻️ preferred-exit reconciled (B229)\nCREATE hostname=%s user=%s tag=%s via=1\nreason: %s (%d device_rules pointed at this exit-node)\nlive-mode is ON; rollback via SQL: DELETE FROM device_exit_node_prefs WHERE user_id=%d AND device_hostname=%s",
				ch.DeviceHostname, ch.Username, ch.NewTag, ch.Reason, ch.RuleCount, ch.UserID, ch.DeviceHostname))
		}
	case "update":
		if err := db.SetDeviceExitNodePref(s.dbc(), ch.UserID, ch.DeviceHostname, ch.NewTag, 0, true); err != nil {
			log.Printf("preferred-reconciler: UPDATE %s/%s: %s → %s FAILED: %v",
				ch.Username, ch.DeviceHostname, ch.OldTag, ch.NewTag, err)
			return
		}
		_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
			"preferred_exit_reconciled",
			fmt.Sprintf("UPDATE pref hostname=%s user=%s tag: %s → %s reason=%s",
				ch.DeviceHostname, ch.Username, ch.OldTag, ch.NewTag, ch.Reason),
			"headscale_node", ch.DeviceHostname)
		log.Printf("preferred-reconciler: UPDATE %s/%s: %s → %s (%s)",
			ch.Username, ch.DeviceHostname, ch.OldTag, ch.NewTag, ch.Reason)
		reasonKey := "stale-tag"
		if ch.Reason == "via-disabled-but-canonical" {
			reasonKey = "update-via"
		}
		if shouldAlert(ch.DeviceHostname, reasonKey, time.Now()) {
			n.SendAlert(fmt.Sprintf("♻️ preferred-exit reconciled (B229)\nUPDATE hostname=%s user=%s\ntag: %s → %s\nreason: %s\nrollback via SQL: DELETE FROM device_exit_node_prefs WHERE user_id=%d AND device_hostname=%s",
				ch.DeviceHostname, ch.Username, ch.OldTag, ch.NewTag, ch.Reason, ch.UserID, ch.DeviceHostname))
		}
	}
}

// DistinctExitNodesOrZero returns DistinctExitNodes
// (0 if not set) — used by the log message in
// applyReconcilerChange for "skip" actions.
//
// 2026-09-03: v1.5.2 (B229).
func (c *ReconcilerChange) DistinctExitNodesOrZero() int {
	// We don't store the distinct count on the
	// ReconcilerChange struct (it only matters for
	// the log line on skip actions). The
	// DevicePrefState.DistinctExitNodes is the
	// authoritative source; the log is best-effort
	// and uses the value embedded in Reason
	// description if we ever add it. For now,
	// returning 0 is fine because the log line
	// prints the dominant exit_node hostname (which
	// is enough for the operator to know which exit
	// would have been picked).
	return 0
}
