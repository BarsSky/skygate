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
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
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
//   - EnsureTagOwner(tag, owners) — B245: pre-populate
//     a brand-new tag in headscale's tagOwners before
//     AddTag. Idempotent (no-op if tag is already
//     listed). Closes the cyborg/2026-09-15 gap where
//     the autoupdater was stuck because the dev-tag
//     didn't exist in tagOwners yet.
//   - EnsureTagOwners(wants) — B272.7 (v1.5.35): the same
//     operation for SEVERAL tags in ONE policy write. The
//     reconciler calls it once per pass, because a
//     per-device write is a read-modify-write of the whole
//     policy and the privileged applier runs asynchronously:
//     live on `aro` three devices needed three dev-tags and
//     only the first survived (`applied=1 failed=2`).
type nodeLister interface {
	InvalidateCache()
	ListAllNodes() ([]headscale.NodeView, error)
	AddTag(nodeID int64, tag string) error
	UntagNode(nodeID int64, tag string) error
	EnsureTagOwner(tag string, owners []string) error
	EnsureTagOwners(wants map[string][]string) error
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
//   - alertSink: B227 observability hook. Every failed
//     AddTag inside Backfill flows through
//     alertSink.ReportFailure — which increments the
//     skygate_tag_autoupdate_failures_total Prometheus
//     counter, writes a tag.autoupdate_failed audit_log
//     row, and (rate-limited) sends a Telegram alert.
//     nil is allowed (defensive — manual callers that
//     want silent backfill can pass nil).
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
func AutoBackfill(ctx context.Context, dbConn db.DBSource, hs nodeLister, alertSink *TagAlertSink, interval time.Duration) {
	if interval <= 0 {
		log.Printf("node-discovery: SKYGATE_NODE_DISCOVERY_INTERVAL=%v, skipping autoupdater goroutine", interval)
		return
	}
	if dbConn == nil {
		log.Printf("node-discovery: nil *db.ResettableDB, skipping autoupdater goroutine (defensive guard)")
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
			runOneTick(ctx, dbConn, hs, alertSink)
		}
	}
}

// runOneTick is the inner loop body — extracted so
// AutoBackfill itself stays small and the unit tests
// can exercise the per-tick behavior without spinning
// up a real ticker.
//
// Behavior:
//  1. List every portal user from the DB
//  2. List every headscale node (one API call, reused)
//  3. For each user, call Backfill(db, hs, nodes, u.ID, u.Username, alertSink)
//  4. Run BackfillInfra to attribute skygate-host-* nodes to the
//     'infra' user (v0.33.1.41, Issue 4 — separate from per-portal-
//     user backfill because 'infra' is a system user, not a real
//     portal account).
//  5. Log a single line with totals at the end
//
// `runOneTick` never returns an error — failures are
// logged and the loop continues. The intent is
// availability: a transient headscale API hiccup or DB
// error should not block subsequent ticks.
func runOneTick(ctx context.Context, dbConn db.DBSource, hs nodeLister, alertSink *TagAlertSink) {
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
	users, err := db.GetAllPortalUsers(dbConn.Current())
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
		Backfill(dbConn, hs, nodes, u.ID, u.Username, alertSink)
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
	// B272: then reconcile DATABASE → headscale, so a tag that never made it
	// onto the node (a failed apply, a file-mode policy reject, a native
	// install without docker) is repaired instead of skipped forever.
	if rows, err := db.ListNodeOwnersAll(dbConn.Current()); err != nil {
		log.Printf("tag-reconcile: list node_owner_map: %v (skipping pass)", err)
	} else {
		ReconcileTags(dbConn, hs, nodes, rows, os.Getenv("SKYGATE_BASE_DOMAIN"), alertSink)
	}
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
//  1. Node has any `tag:dev-infra-*` tag — explicit
//     infra ownership marker (the B77 autoupdater
//     sets this when the B77 Strategy D matches an
//     infra node; future migrations may set it
//     programmatically).
//  2. Node hostname equals "skygate-host" (B251: the
//     pre-B251 `skygate-host-` prefix was too wide —
//     it matched `skygate-host-1`, `skygate-host-1-1`,
//     and any other suffix the admin tenant might add
//     during migration, letting two physical VMs claim
//     the reserved role). The INSERT OR IGNORE handles
//     the "no row yet" check. Captures the single skygate
//     VM (whose Tailscale hostname is set by
//     SKYGATE_TS_HOSTNAME; defaults to `skygate-host`)
//     even when its existing node_owner_map row is owned
//     by a different portal user from the previous era.
//
// Why both rules:
//   - Rule 1 covers FUTURE nodes the operator marks
//     with `tag:dev-infra-*`.
//   - Rule 2 covers the LIVE skygate VM (hostname
//     `skygate-host`) whose tag is still skyadmin-owned
//     from a pre-B251 era (e.g. `tag:dev-skyadmin-skygate-host-1`
//     when the operator used `skygate-host-1` and never
//     re-tagged after the B251 rename). Without rule 2,
//     that node would stay owned by 'skyadmin' and the
//     per-infra ACL grant wouldn't apply.
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
func BackfillInfra(dbConn db.DBSource, nodes []headscale.NodeView) {
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
	if err := dbConn.Current().QueryRow(
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
	matched := 0
	reattributed := 0
	for _, n := range nodes {
		if !isInfraNode(n) {
			continue
		}
		matched++
		// 2026-08-13: v1.3.11 (B111) — Re-attribute existing
		// rows from user-portal buckets (skyadmin, michail,
		// svyatoslava, etc.) to the 'infra' user when the
		// node matches isInfraNode. The original v0.33.1.41
		// logic used INSERT OR IGNORE which preserved the
		// existing owner; that worked for skygate-host-1
		// (a fresh node) but left exit nodes (emilia,
		// karolina, sharlotta, <polygon-vm-hostname>) stranded in
		// the user-portal bucket from a B69/B89 backfill.
		// Without this UPDATE, the per-infra public-access
		// grants (added in B111) miss the exit nodes, and
		// the per-device mesh in 'infra' is empty (the
		// generator skips users with <2 devices).
		//
		// Safety: only update rows that are CURRENTLY in a
		// user-portal bucket (skyadmin/michail/guest/
		// daniil/svyatoslava — i.e. the portal_users that
		// have headscale_user_id IS NOT NULL and are not
		// 'infra'). Rows with an operator-set custom owner
		// (anything else) are preserved.
		//
		// The new tag is `tag:dev-infra-<hostname>` — the
		// future headscale tag the operator will set on
		// this node. Until the operator re-tags, the
		// policy has grants for `tag:dev-infra-emilia`
		// that match no device (emilia still has
		// `tag:dev-skyadmin-emilia`). The grants become
		// live the moment the operator re-tags the node.
		//
		// B265 (2026-09-19): lowercase the hostname, matching the
		// convention every other tag mint uses (nodeownership.go's
		// `tag:dev-<user>-<lower(host)>` and db.GetPerUserDeviceTags'
		// LOWER(hostname)). Without it, a node with an uppercase
		// hostname got a `tag:dev-infra-MyRelay` row while
		// isInfraNode / the ACL's `src=* → dst:tag:dev-infra-<host>`
		// catch-all and the infra tagOwners entry used the lowercase
		// form — so the public-access grant for that exit node
		// matched nothing.
		newTag := "tag:dev-infra-" + strings.ToLower(n.Hostname)
		res, err := dbConn.Current().Exec(
			`UPDATE node_owner_map
			    SET username = 'infra',
			        headscale_user_id = $1,
			        tag = $2,
			        hostname = $3,
			        tagged_by_user_id = $4,
			        tagged_at = `+db.NowUnixSQL()+`
			  WHERE node_id = $5
			    AND username IN (
			        SELECT username FROM portal_users
			         WHERE username != 'infra'
			           AND headscale_user_id IS NOT NULL
			    )`,
			infraHSID.Int64, newTag, n.Hostname, infraPortalID.Int64, n.ID,
		)
		if err != nil {
			log.Printf("infra-backfill: update %s (%s): %v", n.ID, n.Hostname, err)
			continue
		}
		rows, _ := res.RowsAffected()
		if rows > 0 {
			reattributed++
			log.Printf("infra-backfill: re-attributed node_id=%s hostname=%s → username=infra tag=%s", n.ID, n.Hostname, newTag)
		}
		// If the row didn't update (e.g. node not in
		// node_owner_map yet, or already owned by 'infra'
		// / 'tagged-devices' / operator-set), try the
		// INSERT. The INSERT OR IGNORE preserves any
		// existing row (idempotent) and adds the new row
		// when missing.
		_ = db.InsertIgnoreNodeOwnerWithHostname(
			dbConn.Current(), n.ID, infraHSID.Int64, "infra",
			newTag, n.Hostname, infraPortalID.Int64,
		)
	}
	if matched > 0 {
		log.Printf("infra-backfill: %d node(s) matched isInfraNode, %d re-attributed from user-portal to infra", matched, reattributed)
	}
}

// ErrNoStrategyMatch is reported when a headscale node could not be
// attributed to ANY portal user (B272). It is not a failure of skygate: the
// node is either brand new (registered outside the skygate flow and not yet
// adopted by an operator) or owned by a headscale user that has no portal
// account. Pre-B272 this produced NO signal at all — the device had no
// per-device ACL rule and nothing anywhere said so.
var ErrNoStrategyMatch = errors.New("node matched no ownership strategy (register it through skygate, or adopt it on /admin/devices)")

// TagReconcileResult summarises one reconcile pass (B272).
type TagReconcileResult struct {
	Checked  int // database rows inspected
	Applied  int // tags actually (re-)applied to headscale
	Failed   int // applies that headscale refused
	Missing  int // rows whose node is gone from headscale
	Unattrib int // live nodes attributed to no portal user
}

// ReconcileTags makes headscale match the DATABASE (B272).
//
// Why this exists: B77's Backfill walks headscale and only considers nodes
// that ALREADY carry a `tag:dev-…` tag. When the tag apply fails once — an
// ACL/policy reject, docker missing on a native host, a 5xx from the policy
// API — the node has no dev-tag, so every subsequent tick skips it and
// `node_owner_map` keeps claiming a tag the node does not have. Live case
// (2026-09-19): /my/devices showed `tag:dev-daniil-workpc` while
// `headscale nodes list` showed no tags on that node, `tag.autoupdate_failed`
// was empty and the metric had never been incremented.
//
// This pass closes the loop in the other direction: for every database row,
// if headscale does not carry the row's tag, apply it (AddTag preserves the
// node's other tags) and report failures through the B227 sink with the
// reason "tag_missing". Nodes with no dev-tag AND no database row are
// reported once per rate-limit window as "no_strategy" so the operator sees
// devices that no per-device rule can ever match.
//
// `owners` is passed in (rather than read here) so the matching logic is
// unit-testable without a database; the caller loads it with db.ListNodeOwnersAll.
//
// B272.1 (same live host, one tick later): AddTag alone is not enough. headscale
// answers `400 requested tags [...] are invalid or not permitted` for a tag that
// is not listed in the policy's tagOwners — the B245 chicken-and-egg. So before
// applying a tag this pass ensures the tag HAS an owner, taken from the row's
// own username (`<user>@<baseDomain>` + `tagged-devices@<baseDomain>`, exactly
// what the per-user backfill uses). Without this the reconciler reported the
// right node and the right tag forever and could never fix it.
func ReconcileTags(dbConn db.DBSource, hs nodeLister, nodes []headscale.NodeView, rows []db.NodeOwner, baseDomain string, alertSink *TagAlertSink) TagReconcileResult {
	var res TagReconcileResult
	if hs == nil {
		return res
	}
	if dbConn == nil {
		log.Printf("tag-reconcile: no DB source (skipping pass)")
		return res
	}

	byID := make(map[string]headscale.NodeView, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	// Tags whose ownership was already ensured in this pass (the owner list is
	// per tag, and several nodes can share one).
	ensured := map[string]bool{}

	// B272.7 (v1.5.35): make EVERY tag this pass is about to apply permitted in
	// ONE policy write, before the first AddTag. A per-device write is a
	// read-modify-write of the whole policy handed to an asynchronous applier,
	// so N devices used to mean N writes from N stale snapshots — live on `aro`
	// three devices needed three tags and only the first landed. A batch failure
	// is not fatal: the per-row path below retries each tag on its own and
	// reports the specific refusal through the B227 sink.
	if err := ensureTagOwnersBatch(hs, rows, byID, baseDomain, ensured); err != nil {
		log.Printf("tag-reconcile: batch tagOwners pass failed (%v) — falling back to one policy write per tag", err)
	}

	for _, r := range rows {
		if r.Tag == "" {
			continue
		}
		res.Checked++
		n, live := byID[r.NodeID]
		if !live {
			res.Missing++
			continue // deleted in headscale; the per-user pass GCs the row
		}
		// B272.5: heal an empty hostname in node_owner_map.
		//
		// Live on aro: `2||daniil|tag:dev-daniil-workpc` — the row carried the
		// tag but no hostname, because the adopt path writes through
		// UpsertNodeOwner, which has no hostname argument at all. Everything
		// that resolves a node's identity from the map (the ACL's
		// deviceTagForRule fallback, /my/devices labels) then works with an
		// empty host and silently degrades. The reconciler already holds both
		// halves — the row and the live node — so repair it here, before the
		// "already in sync" early return below would skip the row forever.
		if strings.TrimSpace(r.Hostname) == "" && n.Hostname != "" && dbConn != nil && dbConn.Current() != nil {
			if _, herr := db.SetNodeOwnerHostnameIfEmpty(dbConn.Current(), r.NodeID, n.Hostname); herr != nil {
				log.Printf("tag-reconcile: could not backfill hostname for node %s: %v", r.NodeID, herr)
			} else {
				log.Printf("tag-reconcile: backfilled hostname %q for node %s (node_owner_map had none)", n.Hostname, r.NodeID)
			}
		}
		if hasTag(n.Tags, r.Tag) {
			continue // already in sync
		}
		id, err := strconv.ParseInt(r.NodeID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		// B272.1: make sure the policy knows this tag before asking headscale
		// to apply it. B272.7: with a short retry — see
		// ensureTagIsPermittedRetry for why `connection refused` here is
		// transient by construction.
		if err := ensureTagIsPermittedRetry(hs, r, baseDomain, ensured); err != nil {
			res.Failed++
			log.Printf("tag-reconcile: cannot make %q permitted for node %s (%s): %v", r.Tag, r.NodeID, n.Hostname, err)
			if alertSink != nil {
				alertSink.ReportFailure(r.NodeID, n.Hostname, r.Tag, err)
			}
			continue
		}
		// B272.7: the SAME transient window applies to the apply itself. The
		// policy write above restarts headscale on a file-mode host, and the
		// batch made that restart happen once for the whole pass — so without a
		// retry here the tick that just permitted N tags can lose all N
		// AddTag calls to a daemon that is still coming up, and the operator
		// waits another five minutes for tags that were already allowed.
		if err := addTagRetry(hs, id, r.Tag); err != nil {
			res.Failed++
			log.Printf("tag-reconcile: node %s (%s) is missing %q in headscale: %v", r.NodeID, n.Hostname, r.Tag, err)
			if alertSink != nil {
				alertSink.ReportFailure(r.NodeID, n.Hostname, r.Tag, err)
			}
			continue
		}
		res.Applied++
		log.Printf("tag-reconcile: applied %q to node %s (%s) — database said it was owned by %q, headscale had %v", r.Tag, r.NodeID, n.Hostname, r.Username, n.Tags)
	}

	// B272.4 — nodes that no per-device rule can ever match.
	for _, n := range nodes {
		if len(n.Tags) > 0 {
			continue
		}
		if _, owned := ownerByNodeID(rows, n.ID); owned {
			continue
		}
		res.Unattrib++
		if alertSink != nil {
			alertSink.ReportFailure(n.ID, n.Hostname, "(none)", ErrNoStrategyMatch)
		}
	}
	if res.Applied > 0 || res.Failed > 0 || res.Unattrib > 0 {
		log.Printf("tag-reconcile: checked=%d applied=%d failed=%d missing=%d unattributed=%d", res.Checked, res.Applied, res.Failed, res.Missing, res.Unattrib)
	}
	return res
}

// hasTag reports whether the tag list already contains want (exact match).
func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// ensureTagIsPermitted makes sure headscale's policy lists `tag` before it is
// applied to a node (B272.1). Without an owner entry headscale refuses with
// `400 requested tags [...] are invalid or not permitted` — the B245 deadlock,
// seen live on the reconciler's first tick.
//
// The owner comes from the database row (`<username>@<baseDomain>` plus
// `tagged-devices@<baseDomain>`, the same pair the per-user backfill uses), so
// the reconciler repairs the policy from the same source of truth it repairs
// the node tags from. `ensured` caches successful calls per tag within a pass.
//
// A missing baseDomain is reported as an error rather than silently skipping:
// without it the tag can never become permitted, and silence is what made this
// class of failure invisible in the first place.
func ensureTagIsPermitted(hs nodeLister, row db.NodeOwner, baseDomain string, ensured map[string]bool) error {
	if ensured[row.Tag] {
		return nil
	}
	owners, err := tagOwnersFor(row, baseDomain)
	if err != nil {
		return err
	}
	if err := hs.EnsureTagOwner(row.Tag, owners); err != nil {
		return fmt.Errorf("ensure tag owner %q for %v: %w", row.Tag, owners, err)
	}
	ensured[row.Tag] = true
	return nil
}

// tagOwnersFor derives the headscale owners of a row's tag: for a per-device tag
// (`tag:dev-<user>-<host>`) the user the TAG names plus the
// `tagged-devices@<baseDomain>` sentinel pool, and for any other tag form the
// row's own username with the same sentinel fallback.
//
// B288 (2026-09-22): the per-device case now goes through the shared
// db.TagOwnersForUser derivation, so this reconciler, the ownership backfill
// (nodeownership.go) and the ACL generator all emit the SAME owner set. They
// used to differ (this function used the row's `username`, which headscale sets
// to the synthetic `tagged-devices` for every tagged node — B287), so an ACL
// apply stripped `<user>@` and the next tag apply was refused with `not
// permitted`.
//
// A missing baseDomain is an error rather than a silent skip: without it the
// tag can never become permitted, and silence is what made this class of
// failure invisible in the first place. A tag whose NAME names no user (a class
// tag such as `tag:private`) keeps the row-derived owner.
func tagOwnersFor(row db.NodeOwner, baseDomain string) ([]string, error) {
	if baseDomain == "" {
		return nil, fmt.Errorf("SKYGATE_BASE_DOMAIN is not set, so the owner of %q cannot be expressed in the policy — set it to the headscale base domain (e.g. tail.example.com)", row.Tag)
	}
	if user, ok := db.PerDeviceTagUser(row.Tag); ok {
		return db.TagOwnersForUser(user, baseDomain)
	}
	user := row.Username
	if user == "" || user == "tagged-devices" {
		// A synthetic owner: the tag belongs to the sentinel pool, which is
		// exactly how an unadopted device is reachable.
		user = "tagged-devices"
	}
	owners := []string{user + "@" + baseDomain}
	if user != "tagged-devices" {
		owners = append(owners, "tagged-devices@"+baseDomain)
	}
	return owners, nil
}

// ensureTagOwnersBatch computes the set of tags this reconcile pass is going to
// apply and permits them all in a single policy write (B272.7, v1.5.35).
//
// The set is deliberately computed with the SAME predicates the main loop uses
// (row has a tag, node is live, headscale does not carry the tag yet, node id
// parses) so the batch can never permit a tag the loop will not apply, and
// never miss one it will. Tags already in `ensured` are skipped, and every tag
// the batch successfully permits is recorded there, which turns the per-row
// `ensureTagIsPermitted` into a no-op for the rest of the pass.
//
// A row whose owners cannot be derived (no base domain) is skipped here instead
// of aborting the batch: one inexpressible tag must not cost every other device
// its tag. The main loop then reports it per row, with the same error text.
func ensureTagOwnersBatch(hs nodeLister, rows []db.NodeOwner, byID map[string]headscale.NodeView, baseDomain string, ensured map[string]bool) error {
	wants := map[string][]string{}
	for _, r := range rows {
		if r.Tag == "" || ensured[r.Tag] {
			continue
		}
		n, live := byID[r.NodeID]
		if !live || hasTag(n.Tags, r.Tag) {
			continue
		}
		if id, err := strconv.ParseInt(r.NodeID, 10, 64); err != nil || id <= 0 {
			continue
		}
		if _, dup := wants[r.Tag]; dup {
			continue
		}
		owners, err := tagOwnersFor(r, baseDomain)
		if err != nil {
			continue
		}
		wants[r.Tag] = owners
	}
	if len(wants) == 0 {
		return nil
	}
	if err := ensureTagOwnersRetry(hs, wants); err != nil {
		return err
	}
	tags := make([]string, 0, len(wants))
	for tag := range wants {
		ensured[tag] = true
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	log.Printf("tag-reconcile: permitted %d tag(s) in one policy write: %s", len(tags), strings.Join(tags, ", "))
	return nil
}

// ensureTagOwnersRetry is ensureTagOwnersBatch's transport half: it repeats the
// batch only for the transient class this code path causes itself (the
// privileged applier restarts headscale, so the next API call can hit
// `connection refused` for a few seconds). Same policy as
// ensureTagIsPermittedRetry: bounded (1s, 2s, 4s), never on a permission
// refusal, never unbounded.
func ensureTagOwnersRetry(hs nodeLister, wants map[string][]string) error {
	err := hs.EnsureTagOwners(wants)
	for attempt, wait := 0, time.Second; err != nil && attempt < 3; attempt, wait = attempt+1, wait*2 {
		if !isTransientHeadscaleDown(err) {
			return err
		}
		log.Printf("tag-reconcile: headscale is not answering (%v) — retrying the tagOwners batch in %s", err, wait)
		time.Sleep(wait)
		err = hs.EnsureTagOwners(wants)
	}
	return err
}

// addTagRetry applies one tag, retrying the SAME transient class as the two
// helpers above — for the same reason: on a file-mode host the policy write
// restarts headscale, so the API call immediately after it can meet a daemon
// that is still starting. Live evidence for the window is the v1.5.32 fix
// (`ensure-tag-owner: get ACL: … connect: connection refused` twice, ten seconds
// before the daemon was healthy again); the apply call is exposed to exactly the
// same moment, and since v1.5.35 the batch deliberately concentrates the restart
// into the same tick as the applies.
//
// A permission refusal (`are invalid or not permitted`) is NOT retried: that is
// an operator problem and the caller reports it with its reason.
func addTagRetry(hs nodeLister, nodeID int64, tag string) error {
	err := hs.AddTag(nodeID, tag)
	for attempt, wait := 0, time.Second; err != nil && attempt < 3; attempt, wait = attempt+1, wait*2 {
		if !isTransientHeadscaleDown(err) {
			return err
		}
		log.Printf("tag-reconcile: headscale is not answering (%v) — retrying %q on node %d in %s", err, tag, nodeID, wait)
		time.Sleep(wait)
		err = hs.AddTag(nodeID, tag)
	}
	return err
}

// ensureTagIsPermittedRetry wraps ensureTagIsPermitted with a short, bounded
// retry for the ONE transient class this code path causes itself: the privileged
// policy applier RESTARTS headscale after writing the policy, so the very next
// API call can hit `connection refused` for a few seconds.
//
// Live on the native host aro the reconciler lost a whole 5-minute tick twice
// with `ensure-tag-owner: get ACL: api: Get http://127.0.0.1:8081/api/v1/policy:
// dial tcp 127.0.0.1:8081: connect: connection refused` (20:46:03 and 20:51:51),
// while the daemon was healthy again ten seconds later — and each lost tick is
// five minutes of a device without its tag. A refused connection here is
// transient BY CONSTRUCTION, so retry briefly (1s, 2s, 4s) and only then report
// a failure.
//
// The retry is deliberately NOT applied to a policy/permission refusal (that is
// an operator problem, not a timing problem) and never loops forever: the worst
// case adds ~7s to a tick that had already decided to write a policy.
func ensureTagIsPermittedRetry(hs nodeLister, row db.NodeOwner, baseDomain string, ensured map[string]bool) error {
	err := ensureTagIsPermitted(hs, row, baseDomain, ensured)
	for attempt, wait := 0, time.Second; err != nil && attempt < 3; attempt, wait = attempt+1, wait*2 {
		if !isTransientHeadscaleDown(err) {
			return err
		}
		log.Printf("tag-reconcile: %s: headscale is not answering (%v) — retrying in %s", row.Tag, err, wait)
		time.Sleep(wait)
		err = ensureTagIsPermitted(hs, row, baseDomain, ensured)
	}
	return err
}

// isTransientHeadscaleDown reports whether the failure is "headscale is not
// answering right now" rather than "headscale refused this". Used to decide
// between a short retry and reporting the failure to the operator.
func isTransientHeadscaleDown(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "context deadline exceeded")
}

// ownerByNodeID reports whether node_owner_map has a row for the node.
func ownerByNodeID(rows []db.NodeOwner, nodeID string) (db.NodeOwner, bool) {
	for _, r := range rows {
		if r.NodeID == nodeID {
			return r, true
		}
	}
	return db.NodeOwner{}, false
}

// isInfraNode — v0.33.1.41 — returns true if the node
// should belong to the 'infra' portal user.
//
// Rules (first match wins):
//  1. Any tag matches `tag:dev-infra-*` — explicit
//     infra ownership.
//  2. Hostname equals "skygate-host" — the skygate VM
//     itself (B251: strict equality; the pre-B251
//     `strings.HasPrefix("skygate-host-")` rule also
//     matched suffixed forms like `skygate-host-1` /
//     `skygate-host-1-1`, which let the operator
//     accidentally create multiple "skygate" VMs and
//     conflated infra-attribution with the cluster's
//     internal naming). The reserved name `skygate-host`
//     is now produced by /admin/tailscale's default
//     (cmd/skygate/main.go SKYGATE_TS_HOSTNAME) and
//     rejected as a duplicate by findUserForHostname
//     when issued for a non-infra headscale user.
//  3. Any tag equals `tag:exit-node` — an exit node
//     (relay VPS that advertises 0.0.0.0/0 + ::/0).
//     Added in v1.3.11 (B111) per operator request:
//     "infra user будет владеть skygate + exit nodes
//     (karolina sharlotta emilia svyatoslava) и давать
//     публичный доступ к exit nodes остальным".
//     Without rule 3, exit nodes stay owned by
//     skyadmin/michail/svyatoslava and the per-infra
//     public-access grants miss them.
func isInfraNode(n headscale.NodeView) bool {
	for _, t := range n.Tags {
		if strings.HasPrefix(t, "tag:dev-infra-") {
			return true
		}
		if t == "tag:exit-node" {
			return true
		}
	}
	return n.Hostname == "skygate-host"
}
