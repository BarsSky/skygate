package nodeownership

// auto.go — the node-discovery autoupdater goroutine.
//
// 2026-08-09: v0.33.1.25 (B77) — addresses Issue 2 from
// the 2026-08-09 operator report. Pre-fix, when a new
// device registered in headscale (e.g. via a Tailscale
// client consuming a skygate-issued preauth key), the
// device did NOT automatically get its
// `tag:dev-<user>-<device>` applied. The tag is what
// the per-device ACL rule (src=tag:dev-<user>-<device>)
// uses to grant autogroup:internet access. Without it,
// the device had NO access until one of:
//   - the owning user visited /my/devices (which calls
//     Backfill per-user, applying the tag)
//   - the admin clicked "Force backfill" on /admin/devices
//     (PostAdminDevicesForceBackfillTags, iterates all
//     users and calls Backfill)
//
// For a single new device this was a UX papercut; for
// users who had set up preauth keys for off-site
// devices, the device came online with internet access
// effectively denied until the user noticed + reported
// the issue. B77 fixes it by running Backfill in a
// background goroutine at SKYGATE_NODE_DISCOVERY_INTERVAL
// (default 5m, same cadence as the DNS autoupdater).
//
// Design notes:
//   - Single goroutine, launched from cmd/skygate/main.go.
//     Iterates every portal user, calls Backfill once
//     per user. Backfill is idempotent (uses INSERT
//     OR IGNORE) so re-running against the same set of
//     nodes is a no-op for the DB; the only side
//     effects are (a) headscale cache invalidation
//     (per-user), and (b) any new ApplyTag / UntagNode
//     calls triggered by a rename detection in the
//     Backfill body.
//   - Initial tick fires after `interval` (not
//     immediately at startup) to avoid racing with the
//     main boot path. Pre-fix, the force-backfill admin
//     button was the only entry point; a startup
//     auto-fire would have re-back-filled all users
//     on every restart, slowing boot. Operators who
//     want startup backfill can hit the admin button
//     after deploy.
//   - 0 interval disables (caller's responsibility —
//     main.go just won't call AutoBackfill). The
//     function itself doesn't interpret the value.

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// nodeLister is the full set of *headscale.Client methods
// that AutoBackfill and Backfill need. It exists as an
// interface (instead of taking *headscale.Client directly)
// so the test suite can pass a fake implementation
// without depending on a real headscale instance. The
// *headscale.Client type satisfies this interface via
// Go's structural typing — no changes needed in the
// headscale package or the main.go call site.
//
// Methods (subset of *headscale.Client):
//   - InvalidateCache() — clear the cached node list
//     (called before each tick so we read fresh data)
//   - ListAllNodes() — fetch the live headscale nodes
//   - AddTag(nodeID, tag) — apply a tag to a device
//     (called when a new node gets its dev-tag, and
//     during a rename when the new dev-tag needs to
//     be added)
//   - UntagNode(nodeID, tag) — remove a tag (called
//     during a rename when the OLD dev-tag is stale)
type nodeLister interface {
	InvalidateCache()
	ListAllNodes() ([]headscale.NodeView, error)
	AddTag(nodeID int64, tag string) error
	UntagNode(nodeID int64, tag string) error
}

// AutoBackfill runs `Backfill` against every portal user
// in a loop, with `interval` between ticks. The function
// returns when `ctx` is cancelled.
//
// Parameters:
//   - ctx: caller-controlled cancellation. The function
//     returns immediately on Done.
//   - db, hs: passed straight to Backfill (the per-user
//     node-ownership helper). hs is the *global* headscale
//     client (same one /admin/devices/force-backfill-tags
//     uses); per-user contexts are handled inside Backfill.
//   - interval: time between ticks. <=0 disables (the
//     function returns without doing anything — caller
//     should not invoke AutoBackfill in this case but the
//     guard is here for defense in depth).
//
// Behavior on error: a tick that errors (e.g. headscale
// API hiccup) is logged + skipped. The next tick still
// fires on schedule. This matches the existing
// autoupdater / exit-node-monitor pattern (transient
// errors are non-fatal, the loop keeps going).
func AutoBackfill(ctx context.Context, dbConn *sql.DB, hs nodeLister, interval time.Duration) {
	if interval <= 0 {
		log.Printf("node-discovery: SKYGATE_NODE_DISCOVERY_INTERVAL=%v, skipping autoupdater goroutine", interval)
		return
	}
	if dbConn == nil {
		log.Printf("node-discovery: nil *sql.DB, skipping autoupdater goroutine (defensive guard)")
		return
	}
	if hs == nil {
		log.Printf("node-discovery: nil *headscale.Client, skipping autoupdater goroutine (defensive guard)")
		return
	}
	log.Printf("node-discovery: starting (interval=%s)", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("node-discovery: context cancelled, exiting")
			return
		case <-ticker.C:
			runOneTick(ctx, dbConn, hs)
		}
	}
}

// runOneTick is the inner loop body — extracted so
// AutoBackfill itself stays small and the unit tests
// can exercise the per-tick behavior without spinning
// up a real ticker.
//
// Behavior:
//   1. List every portal user from the DB
//   2. List every headscale node (one API call, reused)
//   3. For each user, call Backfill(db, hs, nodes, u.ID, u.Username)
//   4. Run BackfillInfra to attribute skygate-host-* nodes to the
//      'infra' user (v0.33.1.41, Issue 4 — separate from per-portal-
//      user backfill because 'infra' is a system user, not a real
//      portal account).
//   5. Log a single line with totals at the end
//
// `runOneTick` never returns an error — failures are
// logged and the loop continues. The intent is
// availability: a transient headscale API hiccup or DB
// error should not block subsequent ticks.
func runOneTick(ctx context.Context, dbConn *sql.DB, hs nodeLister) {
	// Invalidate the headscale node cache before
	// ListAllNodes so we get fresh data — otherwise we'd
	// read stale node lists for `interval` minutes after
	// any external state change. Same pattern as
	// /admin/devices/force-backfill-tags uses.
	hs.InvalidateCache()
	nodes, err := hs.ListAllNodes()
	if err != nil {
		log.Printf("node-discovery: ListAllNodes failed: %v (skipping tick)", err)
		return
	}
	users, err := db.GetAllPortalUsers(dbConn)
	if err != nil {
		log.Printf("node-discovery: GetAllPortalUsers failed: %v (skipping tick)", err)
		return
	}
	processed := 0
	for _, u := range users {
		// Honor cancellation even mid-loop. If a
		// graceful shutdown starts in the middle of
		// a tick, we don't want to keep churning.
		if ctx.Err() != nil {
			return
		}
		if u.Username == "" {
			continue
		}
		Backfill(dbConn, hs, nodes, u.ID, u.Username)
		processed++
	}
	// 2026-08-10: v0.33.1.41 — Issue 4 infra user.
	// Attribute skygate-host-* nodes to the 'infra'
	// portal user so the per-infra ACL grant can match
	// them. Idempotent (INSERT OR IGNORE on the
	// node_id PK) and runs after the per-portal-user
	// pass so the per-user backfill doesn't accidentally
	// steal an infra node first.
	BackfillInfra(dbConn, nodes)
	log.Printf("node-discovery: tick complete (users=%d nodes=%d)", processed, len(nodes))
}

// BackfillInfra — v0.33.1.41 — Issue 4 infra user.
//
// Walks the live headscale node list and inserts
// node_owner_map rows for nodes that should belong to
// the 'infra' portal user (a system account for
// skygate-host-* infrastructure). Idempotent via
// INSERT OR IGNORE on the node_id PK — running it
// twice is a no-op.
//
// Selection rules (first match wins):
//   1. Node has any `tag:dev-infra-*` tag — explicit
//      infra ownership marker (the B77 autoupdater
//      sets this when the B77 Strategy D matches an
//      infra node; future migrations may set it
//      programmatically).
//   2. Node hostname starts with "skygate-host-"
//      AND has no node_owner_map row yet (the
//      INSERT OR IGNORE handles the "no row yet"
//      check). Captures the existing skygate-host-1
//      node, which has tag:dev-skyadmin-skygate-vm
//      (skyadmin owner) but is the actual skygate
//      infrastructure node.
//
// Why both rules:
//   - Rule 1 covers FUTURE nodes the operator marks
//     with `tag:dev-infra-*`.
//   - Rule 2 covers the LIVE skygate-host-1 node
//     that the operator tagged manually with
//     `tag:dev-skyadmin-skygate-vm` (no infra tag
//     present). Without rule 2, that node would
//     stay owned by 'skyadmin' and the per-infra
//     ACL grant wouldn't apply.
//
// The function does NOT move an existing row from
// 'skyadmin' to 'infra' (the INSERT OR IGNORE is
// per-node_id, and the live node already has a
// node_owner_map row with username='skyadmin' from
// the B69/B89 backfills). Moving ownership is an
// operator decision — they can do it via
// /admin/devices or by re-running the B69 force-
// backfill with a different default user. For the
// MVP, leaving the row alone + adding an infra
// grant that ALSO matches `tag:dev-skyadmin-skygate-vm`
// is enough to give the bot the internet access it
// needs.
//
// On error: returns the first error and stops. The
// caller (runOneTick) is fire-and-forget so a
// failure here just means the next tick will retry.
func BackfillInfra(dbConn *sql.DB, nodes []headscale.NodeView) {
	// Look up the 'infra' portal user's id + headscale
	// user id. If the row is missing (V054 didn't run
	// yet, e.g. fresh DB before migration), bail
	// silently. If headscale_user_id is NULL (V054 ran
	// but ensureInfraUser hasn't linked yet), also bail
	// — the per-infra ACL grant can't match without a
	// headscale_user_id, so the node_owner_map row
	// would be useless.
	var infraPortalID sql.NullInt64
	var infraHSID sql.NullInt64
	if err := dbConn.QueryRow(
		`SELECT id, headscale_user_id FROM portal_users WHERE username = 'infra'`,
	).Scan(&infraPortalID, &infraHSID); err != nil {
		if err == sql.ErrNoRows {
			return
		}
		log.Printf("infra-backfill: lookup infra user: %v", err)
		return
	}
	if !infraPortalID.Valid || !infraHSID.Valid || infraHSID.Int64 == 0 {
		// infraHSID.Int64 == 0: defensive check for the
		// test schema where the column is NOT NULL
		// DEFAULT 0 (so a NULL scan becomes a 0). On the
		// real production schema the column is nullable,
		// so the Valid check is enough; the ==0 guard
		// is here for the test paths where the schema
		// disagrees. The per-infra ACL grant can't match
		// without a real hs id (0 is the zero value, not
		// a valid headscale user id), so skipping here
		// matches the production behaviour.
		return
	}
	count := 0
	for _, n := range nodes {
		if !isInfraNode(n) {
			continue
		}
		// InsertIgnoreNodeOwnerWithHostname: idempotent
		// on the node_id PK. If a row already exists
		// (e.g. owned by 'skyadmin' from a prior
		// backfill), the insert is a no-op and the
		// existing row is preserved.
		err := db.InsertIgnoreNodeOwnerWithHostname(
			dbConn, n.ID, infraHSID.Int64, "infra",
			"tag:private", n.Hostname, infraPortalID.Int64,
		)
		if err != nil {
			log.Printf("infra-backfill: insert %s (%s): %v", n.ID, n.Hostname, err)
			continue
		}
		count++
	}
	if count > 0 {
		// count = number of nodes that matched the isInfraNode
		// predicate (skygate-host-* prefix OR tag:dev-infra-*
		// tag) AND for which InsertIgnoreNodeOwnerWithHostname
		// didn't error. Note: most of these will be no-ops at
		// the DB level because the live skygate-host-1 node
		// already has a row from the B69/B89 backfills with
		// username='tagged-devices'. The INSERT OR IGNORE on
		// the node_id PK preserves the existing row. The log
		// message says "matched" rather than "new" to avoid
		// implying a re-attribution happened — the live
		// skygate-host-1 stays owned by 'tagged-devices'
		// (or 'skyadmin' for an older snapshot) until the
		// operator runs a one-shot UPDATE on node_owner_map.
		log.Printf("infra-backfill: %d node(s) matched the infra pattern (INSERT OR IGNORE may have skipped rows that already have an owner)", count)
	}
}

// isInfraNode — v0.33.1.41 — returns true if the node
// should belong to the 'infra' portal user.
//
// Rules (first match wins):
//   1. Any tag matches `tag:dev-infra-*` — explicit
//      infra ownership.
//   2. Hostname starts with "skygate-host-" — the
//      skygate VM itself, regardless of which user
//      currently owns it in headscale.
func isInfraNode(n headscale.NodeView) bool {
	for _, t := range n.Tags {
		if strings.HasPrefix(t, "tag:dev-infra-") {
			return true
		}
	}
	return strings.HasPrefix(n.Hostname, "skygate-host-")
}
