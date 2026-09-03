// Package nodeownership owns the per-/my/devices backfill
// helper — the 250-line function that, on every page load,
// walks the user's headscale nodes, looks for nodes that
// were registered with a skygate-issued preauth key, and
// inserts the corresponding rows in node_owner_map so the
// user sees their own devices in /my/devices.
//
// refactor-v0.30 Phase D2 (2026-07-29): the function
// previously lived in internal/handlers/handlers_node_ownership.go
// as a method on *App. After Phase B step 5b (which moved
// the /my/devices handler into feature/my/devices.go and
// had it call the helper via a BackfillNodeOwnership
// callback on *Service), the handler/feature split was
// the only consumer of the helper. Phase D2 moves the
// helper to its own package so:
//
//   - feature/my can `import "skygate/internal/nodeownership"`
//     and call nodeownership.Backfill(...) directly,
//     instead of going through the App callback indirection.
//   - future consumers (e.g. a sidecar-mode backfill that
//     runs from a Tailscale hook script) don't need a
//     *App reference.
//
// The migration is mechanical:
//
//   handlers.BackfillNodeOwnershipFn (used by feature/my
//   via the Service.BackfillNodeOwnership field) is now a
//   thin wrapper that calls nodeownership.Backfill(d, hs,
//   nodes, userID, username). The function signature is
//   unchanged from the *App method.
package nodeownership

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/subnet"

	dbpkg "skygate/internal/db"
)

// subnetRouterPrefix matches the hostname pattern set by
// the v0.16.7 per-user subnet-router sidecar. A node
// whose hostname starts with this prefix AND the rest
// equals the portal username is THIS user's subnet-router
// (used by hasRouter detection below).
const subnetRouterPrefix = "skygate-subnet-"

// matchOIDCStrategy implements B175 (v1.5.2) — Strategy E
// for OIDC-registered nodes. Returns the matchedTag
// ("tag:private" by default, or firstTagOrFallback if the
// node already carries tags) and ok=true if the node was
// registered via the OIDC flow AND belongs to the current
// portal user.
//
// Detection:
//   - n.PreAuthKeyID == "" distinguishes OIDC nodes from
//     /my/preauth nodes (Strategy A) and operator-issued
//     preauth key nodes (Strategy C). The OIDC flow does
//     not consume a preauth key.
//   - n.UserName == portalUsername is the ownership signal.
//     headscale creates OIDC users with name = OIDC `name`
//     claim, and skygate's OIDC id_token sets
//     `name = entry.Username` (see internal/oidc/token.go:180)
//     so the headscale user's name equals the skygate
//     portal username.
//
// False-positive guards (NOT enforced here — the caller is
// responsible):
//   - The synthetic "tagged-devices" headscale user has
//     name="tagged-devices" which won't equal any portal
//     username (UNIQUE constraint on portal_users.username).
//   - The `otherOwners` map (built earlier in Backfill) is
//     checked at the top of the per-node loop to filter
//     nodes whose headscale user ID is a different portal
//     user; Strategy E is skipped before reaching here.
//
// Extracted as a package-level function so the B175 B-check
// and unit test can exercise the pure condition without
// spinning up a real *sql.DB (PG-only in v1.3.0+).
func matchOIDCStrategy(n headscale.NodeView, portalUsername string) (matchedTag string, ok bool) {
	if portalUsername == "" {
		return "", false
	}
	// OIDC flow signature: no preauth key was used.
	if n.PreAuthKeyID != "" {
		return "", false
	}
	// OIDC user name == portal username → this is our node.
	if n.UserName != portalUsername {
		return "", false
	}
	// Match. Default tag: "tag:private" (the v0.26+ scope
	// convention). If the node already carries tags (e.g.
	// tag:subnet-router from operator-issued preauth key
	// created via headscale CLI), preserve the first one
	// instead of clobbering — same fallback as Strategy A/C.
	if len(n.Tags) > 0 {
		return firstTagOrFallback(n), true
	}
	return "tag:private", true
}

// Backfill walks the live headscale nodes, and for any
// node whose headscale preAuthKey matches one of this
// portal user's preauth_keys, inserts a row in
// node_owner_map (idempotent via INSERT OR IGNORE).
//
// Why this exists:
//   - When a user issues a preauth key via /my/devices, we save the
//     headscale ID in preauth_keys.headscale_preauth_id.
//   - When that key is later consumed by a Tailscale client, the resulting
//     node reports its origin via the headscale API (node.preAuthKey.id).
//   - If the node then gets a tag applied (e.g. tag:private by ACL),
//     headscale reassigns ownership to a synthetic "tagged-devices" user
//     and the live user_id link is lost.
//   - This backfill reconstructs the link from the persisted key, so the
//     node shows up under the original owner in /my/devices and on the
//     user dashboard. Safe to call on every /my/devices load - the IGNORE
//     makes it a no-op once the snapshot exists.
//
// Garbage collection: this function also reconciles the snapshot against
// current reality. If a node that node_owner_map claims the user owns no
// longer exists in headscale (deleted, expired, reaped), the orphan row
// is removed. Without this, a user who deletes their device would keep
// seeing it on the dashboard forever - the original symptom of the
// user1 "0/0" report. The flip side is that a transient headscale API
// hiccup could drop a row; the next successful /my/devices load will
// re-backfill it from preAuthKey, so the blast radius is one page load.
//
// Two strategies, applied in order, first match wins:
//
//   A. Strict join on n.PreAuthKeyID == preauth_keys.headscale_preauth_id.
//      Works for keys whose headscale_preauth_id was captured at issue
//      time. This is the original path from v0.3.9 - fast and accurate,
//      but vulnerable to API response shape changes (a preauth key issued
//      when the response field name shifted will not have a stored
//      headscale_preauth_id, and the node will not match here).
//
//   C. Temporal fallback. If (A) failed AND the node has a non-empty
//      CreatedAt AND the user has at least one preauth key created
//      within 1 hour BEFORE the node's CreatedAt, we attribute the node
//      to that key's owner. The 1-hour window is a safety margin: a
//      user can't physically generate a preauth key, ship it to a remote
//      device, and have that device register with headscale faster
//      than that. If a key was created within the window, it's
//      effectively the only plausible cause. This recovers ownership
//      for keys whose headscale_preauth_id was never captured (the
//      user1 case: 5/7 keys have NULL headscale_preauth_id because
//      the API stopped populating that field on the day they were
//      generated).
//
// Safety: BOTH strategies skip nodes whose current headscale user
// belongs to a *different* portal user. A node that headscale has
// reassigned to "tagged-devices" still has user=tagged-devices there
// (we never override that), and nodes still in someone's namespace
// (user != "tagged-devices") keep their live link. We only insert
// snapshot rows for nodes that headscale has effectively orphaned
// OR for nodes that the user plausibly owns via temporal correlation.
//
// Parameters:
//   db             — open *sql.DB
//   hs             — headscale client (for AddTag calls). May be nil
//                    (the function is a no-op for the AddTag side
//                    effects; the DB writes still happen).
//   nodes          — live headscale nodes (ListAllNodes result).
//   portalUserID   — the user_id from portal_users.
//   portalUsername — the username from portal_users (used to
//                    match snapshot rows + build dev tags).
//
// Pre-D2 callers (still working via the *App wrapper
// `BackfillNodeOwnershipFn` in internal/handlers/handlers_export.go):
//   - feature/my/devices.go (via the Service.BackfillNodeOwnership
//     callback, set in cmd/skygate/main.go).
func Backfill(
	db dbpkg.DBSource,
	hs nodeLister,
	nodes []headscale.NodeView,
	portalUserID int64,
	portalUsername string,
) {
	if portalUserID == 0 || portalUsername == "" {
		return
	}
	// 2026-07-21: v0.22.3 — track whether THIS user has a
	// subnet-router currently registered in headscale, so
	// subnet.SyncStatus below can compute the right status
	// (active vs router_active). Detection mirrors
	// sidecar.UsernameFromHostname: hostname starts with
	// "skygate-subnet-" and the rest equals our portal
	// username.
	hasRouter := false
	// Build a set of currently-live node IDs.
	live := map[string]bool{}
	for _, n := range nodes {
		live[n.ID] = true
		if !hasRouter && hasRouterTag(n.Tags) && strings.HasPrefix(n.Hostname, subnetRouterPrefix) {
			if uname := strings.TrimPrefix(n.Hostname, subnetRouterPrefix); uname == portalUsername {
				hasRouter = true
			}
		}
	}
	// GC pass: drop snapshot rows for nodes that no longer exist in
	// headscale. Restricted to rows that this portal user owns, so a
	// row owned by a different portal user (and pointing at the same
	// node id, possible if a node was re-tagged under someone else)
	// is left alone.
	// 2026-07-12: Этап 10 part 4 — both queries moved to
	// db.ListNodeOwnerNodeIDsByUsername + db.DeleteNodeOwnerByID.
	snapNodeIDs, _ := dbpkg.ListNodeOwnerNodeIDsByUsername(db.Current(), portalUsername)
	for _, nid := range snapNodeIDs {
		if !live[nid] {
			_ = dbpkg.DeleteNodeOwnerByID(db.Current(), nid, portalUsername)
		}
	}
	// 2026-08-09: v0.33.1.20 — also load the FULL rows so the
	// per-node loop below can detect a hostname rename
	// (existing.hostname != n.Hostname). Pre-fix, the backfill
	// only INSERT-OR-IGNOREd, so a stale `desktop-cj8t9me` row
	// stayed in node_owner_map forever after the user renamed
	// the device to `cyborg`, and headscale accumulated BOTH
	// `tag:dev-<user>-desktop-cj8t9me` AND
	// `tag:dev-<user>-cyborg` (AddTag never removes). The
	// rename detection below pairs UntagNode(oldTag) with the
	// existing AddTag(newTag) so the stale tag actually
	// disappears from headscale.
	existingRows, _ := dbpkg.ListNodeOwnersByUsername(db.Current(), portalUsername)
	existingByNodeID := make(map[string]dbpkg.NodeOwner, len(existingRows))
	for _, r := range existingRows {
		existingByNodeID[r.NodeID] = r
	}
	// Preload this user's preauth keys once.
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.ListPreauthKeysByUser.
	// We use the full row even though only (ID, HeadscalePreauthID,
	// CreatedAt) feed the temporal-match logic. The full struct keeps
	// the helper single-purpose; the unused fields are zero-cost.
	paks, err := dbpkg.ListPreauthKeysByUser(db.Current(), portalUserID)
	if err != nil {
		return
	}
	// Look up the headscale user IDs that other portal users own,
	// so we can detect "this node is currently in someone else's
	// namespace" and refuse to steal it. A node whose n.UserID maps
	// to a different portal user is theirs, not ours.
	otherOwners := map[string]bool{}
	{
		// 2026-07-11: Этап 10 part 1 — moved to db.GetOtherHSUserIDs.
		ids, _ := dbpkg.GetOtherHSUserIDs(db.Current(), portalUserID)
		for _, hid := range ids {
			if hid != "" {
				otherOwners[hid] = true
			}
		}
	}
	// Track nodes we've already snapshotted in this pass so a node
	// doesn't get two snapshot rows (e.g. matching (A) AND (C)).
	inserted := map[string]bool{}
	for _, n := range nodes {
		if inserted[n.ID] {
			continue
		}
		// Refuse to steal a node that headscale currently has in
		// another portal user's namespace. tagged-devices is a
		// synthetic user created by headscale for tag-bearing
		// nodes, NOT a portal user, so it doesn't appear in
		// otherOwners and is fair game for snapshot rows.
		if n.UserID != "" && otherOwners[n.UserID] {
			continue
		}
		var matchedTag string
		// Strategy A: strict join on headscale_preauth_id.
		if n.PreAuthKeyID != "" {
			for _, p := range paks {
				if p.HeadscalePreauthID != "" && p.HeadscalePreauthID == n.PreAuthKeyID {
					// 2026-07-20: v0.22.2 hotfix — same fix as
					// Strategy C below. The preauth key came
					// from skygate (we have its headscale ID
					// in preauth_keys), so the user explicitly
					// registered the device via the skygate
					// /my/preauth flow. The default tag should
					// be tag:private so the device is scoped to
					// this user in headscale's tagOwners + the
					// per-user ACL. Previously firstTagOrFallback(n)
					// returned "tag:untagged" for headscale-tagless
					// nodes (like MSI on 2026-07-20) and the
					// code went to the else branch — InsertIgnoreNodeOwner
					// was called with tag="tag:untagged" AND
					// HS.TagNode(15, "tag:private") was NEVER
					// called, so the node stayed tagless in
					// headscale forever (the snapshot row
					// blocked any further tag:private upgrade
					// because the next backfill would still
					// hit the else branch). The fix: when we
					// have a direct preauth match, default to
					// tag:private. firstTagOrFallback is only
					// used when the node ALREADY has tags (e.g.
					// skygate-host-1 has tag:private in headscale,
					// so firstTagOrFallback returns "tag:private"
					// and the result is unchanged).
					if len(n.Tags) > 0 {
						matchedTag = firstTagOrFallback(n)
					} else {
						matchedTag = "tag:private"
					}
					break
				}
			}
		}
		// Strategy C: temporal fallback. Node has CreatedAt, and
		// one of this user's preauth keys was created within the
		// 1-hour window before the node.
		if matchedTag == "" && n.CreatedAt != "" {
			if nodeAt, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
				bestKey := int64(0)
				bestDelta := time.Duration(0)
				for _, p := range paks {
					keyAt := time.Unix(p.CreatedAt, 0)
					delta := nodeAt.Sub(keyAt)
					// Preauth key must be created BEFORE the node
					// (delta >= 0), and within 1 hour. The user
					// can issue a key, send it to a device, and
					// have the device register - but not faster
					// than ~minute for a remote network, and we
					// want a wide enough window to absorb clock
					// skew, retries, slow SSH tunnels, etc.
					if delta < 0 || delta > time.Hour {
						continue
					}
					if bestKey == 0 || delta < bestDelta {
						bestKey = p.ID
						bestDelta = delta
					}
				}
				if bestKey != 0 {
					// 2026-07-10: bug fix — when the match came through a skygate-issued preauth
					// key, the node must have been registered BY our user. Default to
					// tag:private (so the user only sees their own devices in Tailscale).
					// Previously firstTagOrFallback(n) returned tag:untagged for
					// headscale-tagless nodes — UI showed tag:private locally but
					// headscale had no tag. Admins can still set tag:public manually
					// via /admin/devices/taged (PostAdminNodeTag).
					//
					// 2026-07-22: v0.26.0 — if the node already carries a
					// non-`tag:private` tag (e.g. tag:subnet-router from the
					// preauth key), DON'T override it with tag:private. The
					// subnet-router flow NEEDS tag:subnet-router to stay on
					// the node so the v0.17.0 ACL rule (user → tag:subnet-router →
					// user_subnet:*) keeps working. Mirrors the Strategy A
					// branch above. Caught by the e2e subnet-router pilot
					// on 2026-07-22: skygate-subnet-admin (id=25)
					// registered with tag:subnet-router, the backfill
					// clobbered it to [tag:private] via the destructive
					// TagNode call below. AddTag (also v0.26.0) is the
					// second half of the fix — the call now uses
					// AddTag so even if Strategy C fires, the existing
					// tag:subnet-router is preserved (we append
					// tag:private, don't replace).
					if len(n.Tags) > 0 {
						matchedTag = firstTagOrFallback(n)
					} else {
						matchedTag = "tag:private"
					}
				}
			}
		}
		if matchedTag == "" {
			// 2026-08-10: v0.33.1.37 — Strategy D (B77 follow-up):
			// tag-based username fallback. The pre-fix
			// Backfill only matched nodes registered via
			// /my/preauth (Strategy A: PreAuthKeyID match
			// in the local preauth_keys table) or within
			// 1 hour of a /my/preauth key creation (Strategy
			// C: temporal window). Nodes registered with
			// operator-issued preauth keys (e.g. the
			// skygate-host-1 node created via
			// `headscale preauthkeys create --user 1
			// --reusable --expiration 720h`) are NOT in the
			// preauth_keys table, so neither strategy fires,
			// and the node stays orphaned in
			// node_owner_map (no per-user ACL grant) until
			// the operator manually applies the
			// tag:dev-<user>-<device> tag via
			// `headscale nodes tag -i <id> -t
			// 'tag:dev-<user>-<skygate-vm>' --force`.
			//
			// Strategy D closes this gap: if the node ALREADY
			// has a tag:dev-<username>-* tag in headscale
			// (either auto-applied by a manual
			// `headscale nodes tag` call or by another
			// backfill path), AND the <username> portion
			// matches the current portal user's Username,
			// we treat the node as owned by this user. The
			// tag was already applied at the headscale side
			// (we don't re-apply), but we DO insert the
			// node_owner_map row so the per-user ACL rule
			// (src=tag:dev-<user>-<device>) can match.
			//
			// Why this is safe:
			//   - We only match when the tag's <username>
			//     portion equals the current portal user's
			//     Username, so we never "steal" a node
			//     owned by another user.
			//   - The "refuse to steal" check above
			//     (otherOwners) already filters nodes whose
			//     UserID is a different portal user.
			//   - The tag:subnet-router filter (hasRouter
			//     and the subnetRouterPrefix check) keeps
			//     subnet-router nodes out of the user-grant
			//     path.
			//   - We only INSERT (InsertIgnoreNodeOwner
			//     respects PK uniqueness on node_id), never
			//     UPDATE an existing row, so we never
			//     clobber an existing owner.
			const devTagPrefix = "tag:dev-"
			if hasRouterTag(n.Tags) {
				continue
			}
			for _, t := range n.Tags {
				if !strings.HasPrefix(t, devTagPrefix) {
					continue
				}
				rest := strings.TrimPrefix(t, devTagPrefix)
				// rest is "<user>-<device>". The
				// separator is the FIRST "-"; everything
				// after is the device hostname (which
				// can contain dashes, e.g.
				// "desktop-cuo0tfb").
				idx := strings.Index(rest, "-")
				if idx <= 0 {
					continue
				}
				tagUser := rest[:idx]
				if tagUser == portalUsername {
					matchedTag = t
					break
				}
			}
			if matchedTag == "" {
				// 2026-08-25: v1.5.2 (B175) — Strategy E
				// for OIDC-registered nodes. The
				// pre-B175 backfill only matched nodes
				// via:
				//   A. PreAuthKeyID = preauth_keys.headscale_preauth_id
				//      (catches /my/preauth flow)
				//   C. CreatedAt within 1h of a preauth key
				//      (temporal fallback for A)
				//   D. Existing tag:dev-<user>-* tag
				//      (post-hoc, after operator manually
				//      applied the tag)
				//
				// None of those fire for a node registered
				// via the OIDC flow: there is NO preauth
				// key (the Tailscale client used
				// `tailscale up --login-server
				// https://head.skynas.ru`), the
				// preauth_keys table has no row for this
				// OIDC user, and the node has no tags yet
				// (that's exactly what we're trying to
				// apply).
				//
				// Result pre-B175: the OIDC node stays
				// orphaned in node_owner_map, the
				// per-device dev-tag is never applied,
				// and /my/devices shows the device with
				// "⏳ pending" forever (the operator had
				// to hit "Force backfill" on
				// /admin/devices, or `headscale nodes tag
				// --force` manually, to clear it).
				//
				// B175 closes this gap by matching on
				// n.UserName directly. headscale creates
				// the OIDC user with name = OIDC `name`
				// claim = skygate username
				// (internal/oidc/token.go:180 sets
				// `name = entry.Username`). So
				// `n.UserName == portalUsername` is
				// authoritative ownership for the OIDC
				// path.
				//
				// Safety:
				//   - `n.PreAuthKeyID == ""` distinguishes
				//     OIDC nodes from /my/preauth nodes
				//     (Strategy A) and from operator-issued
				//     preauth key nodes (Strategy C). If
				//     the operator later sets up
				//     `headscale preauthkeys create`
				//     directly via CLI for an OIDC user,
				//     Strategy A/C fires first and E is
				//     skipped.
				//   - The synthetic "tagged-devices" headscale
				//     user has name="tagged-devices" which
				//     doesn't match any portal username
				//     (UNIQUE constraint on
				//     portal_users.username + semantically
				//     different string).
				//   - The `otherOwners` check at the top
				//     of the per-node loop already
				//     filters nodes whose headscale user
				//     ID is a different portal user — so a
				//     name collision between two portal
				//     users (e.g. an admin renames user A
				//     to match user B's name) doesn't
				//     cause cross-ownership.
				//   - InsertIgnoreNodeOwner is
				//     idempotent on node_id PK, so a
				//     re-tick is a no-op.
				//
				// What B175 does NOT cover (out of scope):
				//   - Operators who configure headscale
				//     `oidc.claim_map: { sub: "email" }`
				//     would create OIDC users with name =
				//     email local-part, not the skygate
				//     username — Strategy E would NOT
				//     match. B174.1+ (deferred per
				//     operator) adds a real email column
				//     to portal_users + claim_map-aware
				//     matching.
				if newTag, ok := matchOIDCStrategy(n, portalUsername); ok {
					matchedTag = newTag
				}
				if matchedTag == "" {
					continue
				}
			}
		}
		if matchedTag == "tag:private" {
			// 2026-07-12: bug fix — SKYWORKER (id=9) disappeared from
			// admin's /my/devices because the original a7aeb40 fix
			// replaced INSERT OR IGNORE with UPDATE-only, which is a
			// no-op when no row exists. For new nodes the backfill
			// must INSERT first; the UPDATE then upgrades any stale
			// tag:untagged/empty rows. Admin-set tag:public rows are
			// preserved because INSERT OR IGNORE respects the node_id
			// PK (it skips the insert when a row already exists), and
			// the UPDATE's WHERE clause only matches empty/untagged.
			// 2026-07-12: Этап 10 part 4 — both queries moved to
			// db.InsertIgnoreNodeOwner + db.UpgradeStaleNodeOwnerToPrivate.
			// 2026-07-14: Этап 14 v10 — also persist the headscale
			// hostname (or GivenName) so the bot's /my_nodes can
			// show "hostname (node_id) [tag]" instead of the bare
			// node_id. Without this, /my_nodes is a list of opaque
			// node ids the user has to cross-reference with
			// Headplane.
			_ = dbpkg.InsertIgnoreNodeOwnerWithHostname(db.Current(), n.ID, portalUserID, portalUsername, matchedTag, n.Hostname, portalUserID)
			_ = dbpkg.UpgradeStaleNodeOwnerToPrivate(db.Current(), n.ID, matchedTag, portalUserID)
		} else {
			_ = dbpkg.InsertIgnoreNodeOwnerWithHostname(db.Current(), n.ID, portalUserID, portalUsername, matchedTag, n.Hostname, portalUserID)
		}
		// 2026-07-24: v0.28.0 — auto-apply the per-device tag
		// (tag:dev-<user>-<device>) to headscale. The ACL
		// builder uses this tag as src for per-device exit
		// rules (see internal/acl/acl.go); without the tag
		// being actually applied to the device, headscale
		// rejects the policy with "tag not found" on the
		// next ACL re-apply. Idempotent — AddTag is a no-op
		// if the tag is already present. The hostname is the
		// canonical Tailscale GivenName (already on n).
		//
		// 2026-08-09: v0.33.1.20 — also handle the rename
		// scenario. If node_owner_map already has a row for
		// this node with a DIFFERENT hostname (e.g. the user
		// renamed `desktop-cj8t9me` to `cyborg`), the DB row
		// needs its hostname+tag updated (so /admin/devices
		// shows the new name immediately), and headscale
		// needs the stale `tag:dev-<user>-<oldHost>` dropped
		// before the new tag is added (AddTag never removes,
		// so without UntagNode headscale accumulates BOTH
		// tags and the per-device rule src matches both
		// old+new until the next ACL re-apply). The DB
		// update runs even when hs is nil (the next
		// /my/devices load that DOES have a working hs
		// will then clean up the stale headscale tag).
		if n.Hostname != "" {
			// B176 (v1.5.2): headscale 0.29 rejects tags
			// that contain uppercase letters ("Error:
			// setting tags: rpc error: tag should be
			// lowercase"). The hostname from headscale
			// can have any case (e.g. "SkyBars"), so
			// we lowercase the dev-tag before sending.
			// The pre-B176 code constructed
			// `tag:dev-skyadmin-SkyBars` which headscale
			// silently rejected via the gRPC `tag
			// should be lowercase` error, and the
			// AddTag helper swallowed it (no warn: log
			// because the AddTag failure happened in a
			// different call site — see the live-verify
			// report for node id=35 on 2026-08-25).
			// Lowercasing here matches the
			// `tag:dev-<user>-<device>` v0.28.0
			// convention used everywhere else in the
			// codebase, where the dev-tag is always
			// lowercase to be headscale-compliant.
			devTag := fmt.Sprintf("tag:dev-%s-%s", portalUsername, strings.ToLower(n.Hostname))
			// B177 (v1.5.2): defensive dev-tag rename
			// order. The pre-B177 code did
			// hs.UntagNode(oldDevTag) BEFORE
			// hs.AddTag(devTag), which is destructive
			// when the new tag can't be applied
			// (e.g. headscale ACL rejects
			// `tag:dev-<user>-<newHost>` because the
			// tag has never been seen by headscale
			// before). The old dev-tag was already
			// gone, leaving the node with no dev-tag
			// until manual operator intervention
			// (live-verified on 2026-08-25 for node
			// id=35, Android Secure Folder SkyBars
			// renamed from skybars-1 to skybars-secure
			// via `headscale nodes rename`).
			//
			// B177 swaps the order: AddTag the new
			// dev-tag FIRST. If AddTag fails, the
			// old tag stays intact as a fallback. Only
			// when AddTag succeeds do we UntagNode the
			// old tag. The DB row update is moved
			// inside the success branch so a failed
			// AddTag doesn't leave the row out of sync
			// with headscale.
			if hs != nil {
				if nodeIDInt, err := strconv.ParseInt(n.ID, 10, 64); err == nil {
					if err := hs.AddTag(nodeIDInt, devTag); err != nil {
						// B177: keep existing tags as fallback.
						// The next /my/devices load will retry
						// the AddTag; if the underlying headscale
						// ACL issue is fixed, the tag will be
						// applied then. The old dev-tag (if any)
						// stays on the node so the per-device
						// rule src still works.
						log.Printf("warn: auto-apply dev tag %q to node %s: %v — keeping existing tags as fallback", devTag, n.ID, err)
					} else {
						// AddTag succeeded. Now handle the rename
						// case: if the DB row had an OLD dev-tag
						// (different hostname), drop it so we don't
						// accumulate both old+new tags. The DB row
						// is updated to match.
						if existing, ok := existingByNodeID[n.ID]; ok && existing.Hostname != "" && existing.Hostname != n.Hostname {
							// Rename detected. The existing dev-tag
							// was tag:dev-<user>-<oldHost>; compute
							// it from existing.Tag (the row already
							// stores the right value, so we don't
							// have to guess the format).
							oldDevTag := existing.Tag
							// Defensive: if existing.Tag happens
							// to NOT start with the dev- prefix
							// (e.g. tag:private left over from a
							// pre-v0.28 row), don't try to untag
							// it — that would drop the privacy
							// scope and could leak the device
							// into another portal user's view.
							if oldDevTag != "" && strings.HasPrefix(oldDevTag, "tag:dev-") && oldDevTag != devTag {
								if err := hs.UntagNode(nodeIDInt, oldDevTag); err != nil {
									// Non-fatal: the stale
									// tag stays in headscale,
									// but the per-device
									// rule src still works
									// (the new tag is
									// applied). The next
									// /admin/exit-rules
									// reapply will surface
									// the now-stale
									// tagOwners and the
									// operator can clean
									// up via /admin/devices.
									log.Printf("warn: untag stale dev tag %q on node %s during rename: %v", oldDevTag, n.ID, err)
								} else {
									log.Printf("DBG backfill rename node=%s old_hostname=%s new_hostname=%s untag=%s add=%s",
										n.ID, existing.Hostname, n.Hostname, oldDevTag, devTag)
								}
							}
							// Update DB row to match the new
							// hostname + new dev-tag. This is the
							// v0.33.1.20 fix that lets
							// /admin/devices show the new name
							// immediately (without it, the row
							// stayed at the old hostname until
							// an admin manually ran
							// /admin/devices/sync, which used
							// n.UserID=n.UserName from the live
							// headscale list — also wrong for
							// tagged-devices nodes, since the
							// synthetic tagged-devices user
							// would be assigned).
							if err := dbpkg.UpdateNodeOwnerHostnameAndTag(db.Current(), n.ID, n.Hostname, devTag, portalUserID); err != nil {
								// ErrNodeOwnerNotFound is
								// benign (INSERT-OR-IGNORE
								// just lost the race); any
								// other error means a
								// future /my/devices load
								// will retry.
								if !errors.Is(err, dbpkg.ErrNodeOwnerNotFound) {
									log.Printf("warn: update hostname+tag for node %s: %v", n.ID, err)
								}
							}
							// Refresh the local map so a
							// later loop iteration sees the
							// new value (not strictly needed
							// since node_ids are unique per
							// row, but keeps the in-memory
							// state consistent for any
							// future debugging in this pass).
							existingByNodeID[n.ID] = dbpkg.NodeOwner{
								NodeID:   n.ID,
								Hostname: n.Hostname,
								Tag:      devTag,
								Username: portalUsername,
							}
						}
					}
				}
			}
		}
		// 2026-07-24: v0.28.0 — backfill device_hostname on
		// every device_rule row for this device. The
		// migration v0.44 left device_hostname empty for
		// pre-v0.28.0 rules; this UPDATE flips the ACL
		// src from device_ip to tag:dev-<user>-<device>
		// for any rule whose node we just snapshotted.
		// Idempotent — only writes when hostname is empty
		// or stale. Triggered once per /my/devices load,
		// so the next ACL re-apply picks up the tag.
		_ = dbpkg.UpdateDeviceRuleHostnameForNode(db.Current(), n.ID, n.Hostname)
		// Push tag:private to headscale if matched. Safe for empty/untagged rows.
		// Idempotent: skip if the node already carries tag:private — otherwise every
		// /my/devices load would do an HTTP roundtrip to headscale per device,
		// AND call InvalidateCache() which forces the next /my/devices load to
		// re-fetch everything (the bug that was making the page take ~2s).
		if matchedTag == "tag:private" && hs != nil {
			hasPrivate := false
			for _, t := range n.Tags {
				if t == "tag:private" {
					hasPrivate = true
					break
				}
			}
			// 2026-07-20: v0.22.2 debug log — helps trace the
			// "tag:private disappears after 2nd backfill" symptom
			// (operator saw tags='' in headscale API right after
			// the 2nd backfill returned). The log shows the
			// matchedTag + hasPrivate + whether TagNode was
			// called. Safe to remove once the root cause is
			// pinned (suspect: headscale's HS.ListAllNodes
			// returns a cached snapshot from a different
			// goroutine, and the 2nd backfill sees stale
			// n.Tags=[] while headscale's authoritative state
			// is ['tag:private']).
			log.Printf("DBG backfill node=%s name=%s matchedTag=%s api_tags=%v hasPrivate=%v",
				n.ID, n.Hostname, matchedTag, n.Tags, hasPrivate)
			if !hasPrivate {
				if nodeIDInt, err := strconv.ParseInt(n.ID, 10, 64); err == nil {
					// 2026-07-22: v0.26.0 — was a.HS.TagNode(..., "tag:private")
					// which is destructive (headscale 0.29's `nodes tag
					// --force` REPLACES the entire tag set). If the node
					// already carries a meaningful tag (e.g. tag:subnet-router
					// from the preauth, or tag:exit-node / tag:public set
					// by an admin), that tag was silently wiped. AddTag
					// reads the current tag set first and writes the
					// union, preserving everything else.
					if err := hs.AddTag(nodeIDInt, "tag:private"); err != nil {
						log.Printf("warn: auto-tag node %s: %v", n.ID, err)
					} else {
						log.Printf("DBG backfill AddTag called for node=%s (ensure tag:private)", n.ID)
					}
				}
			}
		}
		inserted[n.ID] = true
		// Mark the preauth key as used if headscale has a node attached to it.
		// 2026-07-11: Этап 10 part 3 — UPDATE moved to db.MarkPreauthKeyUsedByHSID.
		// Best-effort (helper returns error, we log + continue). The
		// helper is a no-op for empty headscaleID, so the n.PreAuthKeyID
		// != "" guard is technically redundant but kept for symmetry
		// with the original inline code and as a fast-path skip.
		if n.PreAuthKeyID != "" {
			if err := dbpkg.MarkPreauthKeyUsedByHSID(db.Current(), n.PreAuthKeyID); err != nil {
				log.Printf("warn: mark key %s used: %v", n.PreAuthKeyID, err)
			}
		}
	}
	// 2026-07-21: v0.22.3 — sync the user_subnets.status to
	// reflect the new state. After every /my/devices load, the
	// user's node_owner_map is up to date, so SyncStatus can
	// safely recompute the status. ErrNotFound is benign
	// (user has no subnet row → not opted in → nothing to
	// sync); every other error gets a warn log so we can
	// spot regressions in production.
	newStatus, syncErr := subnet.SyncStatus(db.Current(), portalUserID, hasRouter)
	if syncErr != nil && !errors.Is(syncErr, subnet.ErrNotFound) {
		log.Printf("warn: subnet.SyncStatus user=%d hasRouter=%v: %v", portalUserID, hasRouter, syncErr)
	} else if syncErr == nil {
		log.Printf("DBG subnet sync user=%d hasRouter=%v status=%s", portalUserID, hasRouter, newStatus)
	}
}

// firstTagOrFallback returns the node's first tag, or "tag:untagged"
// if the node has no tags. Used to populate node_owner_map.tag for
// rows that come from strategies that don't otherwise carry a tag
// (specifically the temporal fallback in C, which fires for both
// tagged and untagged nodes).
func firstTagOrFallback(n headscale.NodeView) string {
	if len(n.Tags) > 0 {
		return n.Tags[0]
	}
	return "tag:untagged"
}

// hasRouterTag is a small slice helper used by the v0.22.3
// subnet status sync logic. Kept private to nodeownership
// since it's only used by Backfill; the sidecar package
// has its own copy of the same logic for clarity.
func hasRouterTag(tags []string) bool {
	for _, t := range tags {
		if t == "tag:subnet-router" {
			return true
		}
	}
	return false
}
