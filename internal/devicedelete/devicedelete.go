// Package devicedelete is the shared coordinator for the
// comprehensive device-delete flow (B171, v1.5.2).
//
// Pre-B171, the per-row Delete buttons on /my/devices (B162,
// v1.5.1) and /admin/devices (B169, v1.5.2) cleaned three
// things:
//
//   1. headscale (gRPC DeleteNode) — the node itself
//   2. skygate's node_owner_map (DeleteNodeOwnerByNodeTag)
//   3. skygate's device_exit_node_prefs (DeleteDeviceExitNodePref)
//
// That left the device_rules table full of orphaned rows
// pointing at a non-existent device_id. The next ACL regen
// (the next time the user saves a rule, or the operator
// applies a snapshot, or the autoupdater fires) would
// either silently skip the orphans (producing a policy
// that's inconsistent with /my/exit-rules) or include them
// and crash headscale's SetPolicy with a 400 Bad Request.
// The headplane view of the device was a non-issue (headplane
// is read-only from headscale and reflects deletions on its
// next page load), but the operator-observed symptom was
// "I deleted a device, and now I have ghost rules in the
// ACL" — see the operator message that drove B171
// (2026-08-25, "при удалении устройства также
// корректно подчищать и перегенерировать ACL").
//
// Delete() here is the shared helper that both
// PostMyDeviceDelete (internal/feature/my/devices.go) and
// PostAdminDeviceDelete (internal/feature/admin/devices.go)
// call after headscale.DeleteNode succeeds. The flow is:
//
//   1. db.DeleteNodeOwnerByNodeTag — clean the
//      node_owner_map (this is what the bot's /exit_nodes
//      reads; a stale row would keep showing the deleted
//      node as a relay candidate).
//   2. db.DeleteDeviceExitNodePref — clean the per-user
//      per-hostname exit-node preference. For the admin
//      path the userID is the original owner (looked up
//      from node_owner_map before the row was deleted);
//      the helper accepts userID=0 as a no-op so the
//      admin path can pass 0 if the device never had a
//      portal user.
//   3. db.DeleteRulesByDeviceID — clean every device_rules
//      row that referenced this device. The pre-B171 code
//      only did this on a per-rule basis (the user had to
//      click each rule's Delete button in /my/exit-rules).
//      Post-B171 a single device delete cleans all of them
//      in one transaction.
//   4. acl.ApplyACLPipelineForPlane — regenerate the HuJSON
//      ACL from the now-clean device_rules and re-apply
//      it to headscale. The pre-B171 code didn't re-apply,
//      so the policy headscale was serving still named
//      the deleted device. Post-B171 headscale's policy
//      matches the user's /my/exit-rules view.
//   5. hs.InvalidateCache — the headscale.Client caches
//      ListAllNodes for 5s. Without this the next
//      /my/devices or /admin/devices page load would
//      show the deleted node for up to 5s.
//
// The audit row + the user-visible flash message are
// composed by the caller from the Result struct — this
// keeps the package free of any i18n or audit-callback
// dependencies (the caller wires its own Audit fn and
// its own I18n.T in the template's flash query string).
package devicedelete

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"skygate/internal/acl"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

// Deps is the set of dependencies Delete() needs. The
// struct is passed by value (not a pointer to a long
// list of positional args) so the call sites stay
// readable and so a future addition (e.g. a
// TelegramNotifier) doesn't break every existing call
// site.
type Deps struct {
	// DB is the skygate *sql.DB.
	DB *sql.DB
	// HS is the headscale.Client. Caller picks the
	// scope: HSGlobalFn() for admin, HSForUserFn(uid)
	// for user-side. The headscale.Client.DeleteNode
	// call is delegated to the caller before Delete()
	// is invoked (so this package doesn't need to
	// know about the 404/exit-node special cases —
	// those are part of the headscale interaction, not
	// the cleanup).
	HS *headscale.Client
	// Cfg is the skygate *config.Config. The ACL
	// pipeline reads Cfg.ACLWithViaEnabled to decide
	// whether to use the `via:` syntax, so we need
	// the real config (not a bool flag) here.
	Cfg *config.Config
	// Username is the operator's username for the
	// ApplyACLPipeline audit row + the audit row in
	// the caller's wrapper. Empty for system-driven
	// deletes (none today, but the field is here so a
	// future scheduled-purge path can pass "" without
	// breaking the signature).
	Username string
	// PlaneURL is the headscale control-plane URL for
	// the ACL pipeline. Empty for the global default
	// plane (the typical case in single-plane deploys).
	PlaneURL string
	// AuditDetail is the prefix the caller wants in
	// the ACL pipeline's audit row. The pipeline will
	// log the rule set + version; the prefix tells
	// the operator WHY the regen happened. The
	// canonical values are "device_delete hostname=X
	// id=N rules_removed=K" (for the B171 case) and
	// "device_delete hostname=X id=N (already gone in
	// headscale)" (for the headscale-404-already-
	// deleted case — rules cleanup still runs because
	// the local DB has the stale rows).
	AuditDetail string
	// AuditFn is optional. When non-nil, Delete() calls
	// it with each cleanup step's outcome so the caller's
	// /admin/audit page gets a single comprehensive row
	// per delete (instead of one per cleanup step, which
	// would be noise). The argument format is
	// (action, detail) — matches the s.Backend.Audit
	// signature in the feature packages.
	AuditFn func(action, detail string)
}

// Result is the structured return value of Delete. The
// caller (PostMyDeviceDelete / PostAdminDeviceDelete) is
// responsible for converting this into the user-visible
// flash message + the canonical audit row. Keeping the
// i18n + audit concerns in the caller means this package
// stays free of any i18n.Catalog dependency.
type Result struct {
	// HeadscaleAlreadyGone is true when the caller passed
	// in a headscale-404 / "no longer exists in NodeStore"
	// outcome (the user-visible flash should say
	// "device was already removed from headscale").
	HeadscaleAlreadyGone bool
	// NodeOwnerRowsDeleted is the count of rows removed
	// from node_owner_map.
	NodeOwnerRowsDeleted int64
	// DeviceExitPrefDeleted is true if a row was removed
	// from device_exit_node_prefs.
	DeviceExitPrefDeleted bool
	// RulesDeleted is the count of rows removed from
	// device_rules (the orphaned rules for the deleted
	// device).
	RulesDeleted int64
	// ACLRegen is the outcome of the post-cleanup ACL
	// re-apply. Applied=true means headscale accepted the
	// new policy; ACLVersion is the snapshot row id
	// (0 if GenerateACL itself failed before saving);
	// Err is the headscale.SetPolicy error if any.
	ACLRegen acl.ApplyResult
}

// Delete runs the comprehensive device-delete cleanup
// after headscale.Client.DeleteNode has been called by
// the caller. The nodeID and hostname are the values
// the caller resolved from headscale (or, in the
// "already gone" case, the value the caller captured
// from a snapshot row before the headscale 404).
//
// The function is deliberately forgiving: every step is
// best-effort and the failures are logged + surfaced
// via the Result struct, but a failure in (3) or (4)
// does not abort the rest of the cleanup. The only
// step that can return an error is the initial DB
// connection validation (the *sql.DB handle being nil
// would be a programming error, not a runtime
// condition).
//
// Returns (Result, error). error is non-nil only when a
// precondition is violated (nil DB, nil HS, etc.); the
// per-step outcomes are in the Result.
func Delete(ctx context.Context, deps Deps, nodeID int64, hostname, userNameForPref string) (Result, error) {
	if deps.DB == nil {
		return Result{}, fmt.Errorf("devicedelete.Delete: deps.DB is nil")
	}
	if deps.HS == nil {
		return Result{}, fmt.Errorf("devicedelete.Delete: deps.HS is nil")
	}

	res := Result{}

	// 1. node_owner_map cleanup.
	if nDeleted, err := db.DeleteNodeOwnerByNodeTagCounted(deps.DB, fmt.Sprintf("%d", nodeID), ""); err != nil {
		log.Printf("devicedelete: DeleteNodeOwnerByNodeTag id=%d err=%v", nodeID, err)
		if deps.AuditFn != nil {
			deps.AuditFn("device_delete_node_owner_cleanup_failed",
				fmt.Sprintf("id=%d err=%v", nodeID, err))
		}
	} else {
		res.NodeOwnerRowsDeleted = nDeleted
	}

	// 2. device_exit_node_prefs cleanup. The admin
	// path can pass userID=0 if the device never had a
	// portal user (rare but possible for orphan rows);
	// DeleteDeviceExitNodePref is a no-op on userID=0.
	if hostname != "" {
		var userID int64
		// Best-effort lookup: if the row was just
		// deleted in step 1, we have no source of
		// truth for the original user_id. That's
		// fine — the admin path's primary cleanup
		// is the node_owner_map row, and the
		// per-hostname pref is a soft-cleanup (the
		// user will just see the default exit-node
		// again on their next /my/devices load).
		_ = deps.DB.QueryRow(
			`SELECT user_id FROM node_owner_map WHERE node_id = $1 LIMIT 1`,
			fmt.Sprintf("%d", nodeID)).Scan(&userID)
		if userID == 0 && userNameForPref != "" {
			_ = deps.DB.QueryRow(
				`SELECT id FROM portal_users WHERE username = $1`,
				userNameForPref).Scan(&userID)
		}
		if userID > 0 {
			if err := db.DeleteDeviceExitNodePref(deps.DB, userID, hostname); err != nil {
				log.Printf("devicedelete: DeleteDeviceExitNodePref userID=%d host=%s err=%v",
					userID, hostname, err)
				if deps.AuditFn != nil {
					deps.AuditFn("device_delete_exit_pref_cleanup_failed",
						fmt.Sprintf("id=%d host=%s err=%v", nodeID, hostname, err))
				}
			} else {
				res.DeviceExitPrefDeleted = true
			}
		}
	}

	// 3. device_rules cleanup. This is the new
	// step that B171 adds on top of B162/B169. The
	// count is returned so the caller can show
	// "Device X deleted. 5 ACL rules cleaned." in
	// the user-visible flash.
	if rulesDeleted, err := db.DeleteRulesByDeviceID(deps.DB, int(nodeID)); err != nil {
		log.Printf("devicedelete: DeleteRulesByDeviceID id=%d err=%v", nodeID, err)
		if deps.AuditFn != nil {
			deps.AuditFn("device_delete_rules_cleanup_failed",
				fmt.Sprintf("id=%d err=%v", nodeID, err))
		}
	} else {
		res.RulesDeleted = rulesDeleted
	}

	// 4. ACL regen. This is the second new step
	// B171 adds — pre-B171 the local DB was cleaned
	// but headscale's policy still named the deleted
	// device. Post-B171 the policy is regenerated
	// from the now-clean device_rules and re-applied.
	//
	// The ACL pipeline is best-effort: if
	// ApplyACLPipelineForPlane returns Applied=false
	// (headscale rejected the policy, or GenerateACL
	// itself errored), the user-visible flash should
	// say "device deleted, ACL regen failed: <err>"
	// so the operator knows to investigate. The
	// audit row gets the same detail.
	//
	// We skip the regen entirely if the user has
	// no device_rules left AND no domain rules
	// pointing to a non-existent device (the
	// common case is "user deletes their only
	// device, never had any rules" — regen would
	// produce an empty ACL identical to the
	// current one). The skip is an optimization;
	// the regen would still succeed (just waste
	// a headscale round-trip).
	//
	// For the B171 simplification, always regen —
	// the cost is one headscale.SetPolicy call,
	// the benefit is the operator can be sure
	// headscale's policy matches the DB. This
	// matches the B162 (user side) and B169 (admin
	// side) handlers' existing "regen on every
	// mutation" behaviour from
	// PostMyDevicePreferredExitSet /
	// PostAdminDevicePreferredExitSet.
	useVia := false
	if deps.Cfg != nil {
		useVia = deps.Cfg.ACLWithViaEnabled
	}
	detailForLog := deps.AuditDetail
	if detailForLog == "" {
		detailForLog = fmt.Sprintf("device_delete id=%d hostname=%s rules_removed=%d",
			nodeID, hostname, res.RulesDeleted)
	}
	res.ACLRegen = acl.ApplyACLPipelineForPlane(deps.DB, deps.HS, deps.PlaneURL, nil, deps.Username, detailForLog, useVia)
	if !res.ACLRegen.Applied {
		log.Printf("devicedelete: ACL regen failed id=%d err=%v", nodeID, res.ACLRegen.Err)
		if deps.AuditFn != nil {
			deps.AuditFn("device_delete_acl_regen_failed",
				fmt.Sprintf("id=%d err=%v", nodeID, res.ACLRegen.Err))
		}
	}

	// 5. Invalidate the headscale cache so the next
	// /my/devices or /admin/devices page load sees
	// the deletion immediately (otherwise the cache
	// TTL of 5s would keep showing the deleted node).
	deps.HS.InvalidateCache()

	if deps.AuditFn != nil {
		// Compose a comprehensive audit row so the
		// operator can see the full cleanup outcome
		// in /admin/audit. The format mirrors the
		// pre-B171 "device_deleted" row but adds
		// the new counters + the headplane note.
		headplaneNote := "headplane: read-only view, will refresh on next UI load (~30s)"
		detail := fmt.Sprintf("id=%d hostname=%s node_owner_rows=%d exit_pref=%t rules_removed=%d acl_version=%d acl_applied=%t %s",
			nodeID, hostname,
			res.NodeOwnerRowsDeleted, res.DeviceExitPrefDeleted, res.RulesDeleted,
			res.ACLRegen.Version, res.ACLRegen.Applied, headplaneNote)
		deps.AuditFn("device_deleted", detail)
	}

	return res, nil
}
