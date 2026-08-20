// Package db — SQL query constants.
//
// 2026-07-11: refactor v0.6.0 (Этап 9). Before this file existed, the same
// SQL strings were duplicated across 12+ handlers and telegram command files
// (we counted 57 raw SQL strings in handlers alone). Two practical problems:
//
//  1. Refactor hazard — adding a column to a table meant hunting the
//     string in every handler. The "INSERT INTO exit_rule_logs" pattern
//     appeared 10 times verbatim; subtle drift ("INSERT INTO exit_rule_logs "
//     vs "INSERT INTO  exit_rule_logs ") was possible.
//
//  2. Discoverability — knowing whether `device_rules` had a `parent_domain`
//     column required grep across the handlers package. A change like
//     "rename target_type enum value" had no central location to update.
//
// queries.go groups the canonical SQL strings by table, with package-level
// `const` blocks so callers reference `qInsertExitRuleLog` instead of a
// free-floating string literal. Comment blocks per table document the
// schema (column list) so a reader doesn't have to flip to migrations.go.
//
// IMPORTANT: this file is read-only metadata. The actual query parameters
// (?, ?, ...) are kept inline; the Go SQL driver handles the parameter
// expansion. Do NOT use fmt.Sprintf to build queries from constants — that
// re-introduces SQL injection for the parameters that do vary.
//
// Helper functions that wrap these constants (AppendExitRuleLog,
// MarkACLApplied, etc.) live in the table-named files next to this one.
package db

// ---------------------------------------------------------------
// exit_rule_logs  —  v0.20 migration
//   id           INTEGER PRIMARY KEY AUTOINCREMENT
//   version      INTEGER NOT NULL
//   action       TEXT    NOT NULL  ('apply'|'delete'|'sync'|'rollback'|
//                                   'apply_fail'|'delete_fail'|'rollback_fail'|
//                                   'autoupdate'|'api_bulk')
//   detail       TEXT    DEFAULT ''
//   created_at   INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	qInsertExitRuleLog = `INSERT INTO exit_rule_logs (version, action, detail) VALUES ($1, $2, $3)`
)

// qSelectLastSyncForExitNode returns the most recent sync log line that
// mentions `?` (an exit_node name) in its detail. Used by the admin node
// load dashboard to show "last sync" per exit-node. The detail column is
// free-form text so we LIKE-match.
const qSelectLastSyncForExitNode = `SELECT COALESCE(MAX(created_at), 0) FROM exit_rule_logs WHERE action = 'sync' AND detail LIKE $1`

// qSelectRecentExitRuleLogs powers the admin /admin/exit-rules page top
// panel (latest 20 log lines, newest first).
const qSelectRecentExitRuleLogs = `SELECT version, action, detail, created_at FROM exit_rule_logs ORDER BY id DESC LIMIT 20`

// ---------------------------------------------------------------
// acl_snapshots  —  v0.20 migration
//   id                INTEGER PRIMARY KEY AUTOINCREMENT
//   version           INTEGER NOT NULL
//   config            TEXT    NOT NULL  (BLOB of the headscale HuJSON policy)
//   created_by        TEXT    NOT NULL
//   applied_success   INTEGER DEFAULT NULL  (NULL = pending, 0 = failed, 1 = ok)
//   error_msg         TEXT    DEFAULT ''
//   created_at        INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	// qMaxACLVersion returns the largest version ever assigned; +1 is the
	// next version number for a new snapshot.
	qMaxACLVersion = `SELECT COALESCE(MAX(version), 0) FROM acl_snapshots`

	// qInsertACLSnapshot stores a brand-new snapshot. The version is
	// supplied by the caller (typically NextACLVersion(db)+1) so multiple
	// writers in the same process don't collide on the auto-increment id.
	qInsertACLSnapshot = `INSERT INTO acl_snapshots (version, config, created_by, applied_success) VALUES ($1, $2, $3, 1)`

	// qMarkACLApplied is fired after headscale has accepted the policy.
	qMarkACLApplied = `UPDATE acl_snapshots SET applied_success = 1 WHERE version = $1`

	// qMarkACLFail records a failure reason. error_msg must be non-NULL
	// (the column allows TEXT but headscale error strings can be long).
	qMarkACLFail = `UPDATE acl_snapshots SET applied_success = 0, error_msg = $1 WHERE version = $2`

	// qSelectACLConfig reads the HuJSON policy BLOB for a given version.
	// Rollback handlers feed this back into headscale.
	qSelectACLConfig = `SELECT config FROM acl_snapshots WHERE version = $1`

	// qLastAppliedACLVersion powers the telegram /status command.
	qLastAppliedACLVersion = `SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success = 1`

	// qSelectRecentACLSnapshots powers the admin /admin/exit-rules page
	// (latest 10 snapshots, newest first).
	qSelectRecentACLSnapshots = `SELECT version, created_by, applied_success, error_msg, created_at FROM acl_snapshots ORDER BY version DESC LIMIT 10`
)

// ---------------------------------------------------------------
// audit_log  —  v0.25 migration
//   id           INTEGER PRIMARY KEY AUTOINCREMENT
//   user_id      INTEGER DEFAULT 0
//   username     TEXT    DEFAULT ''
//   action       TEXT    NOT NULL
//   detail       TEXT    DEFAULT ''
//   ip_address   TEXT    DEFAULT ''   (currently unused — left in schema
//                                       so a future change doesn't need a
//                                       migration)
//   created_at   INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	// qInsertAuditLog — used by handlers.audit and the telegram /ack
	// /restart helpers.
	qInsertAuditLog = `INSERT INTO audit_log (user_id, username, action, detail) VALUES ($1, $2, $3, $4)`

	// qSelectAuditActions returns the distinct action values present in
	// audit_log. Used by the admin /admin/audit filter dropdown.
	qSelectAuditActions = `SELECT DISTINCT action FROM audit_log ORDER BY action`
)

// ---------------------------------------------------------------
// portal_users  —  v0.25 migration (bootstrap)
//   id                  INTEGER PRIMARY KEY AUTOINCREMENT
//   username            TEXT UNIQUE NOT NULL
//   password_hash       TEXT NOT NULL
//   is_admin            INTEGER NOT NULL DEFAULT 0
//   headscale_user_id   INTEGER          (FK to headscale user_id, set after HS create)
//   created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now'))
//   theme               TEXT NOT NULL DEFAULT 'linear'
// ---------------------------------------------------------------

const (
	qSelectUserByName      = `SELECT id, password_hash, is_admin FROM portal_users WHERE username = $1`
	qSelectUserIDByName    = `SELECT id FROM portal_users WHERE username = $1`
	qSelectAllPortalUsers  = `SELECT id, username, is_admin, headscale_user_id, created_at, theme, subnet_cidr, subnet_status, subnet_router_node_id FROM portal_users ORDER BY id`
	qSelectPortalUsernames = `SELECT username FROM portal_users ORDER BY id`
	// 2026-07-16: v0.13.0 — per-plane ACL. qSelectPortalUsernamesForPlane
	// returns usernames of every portal user on a given control plane
	// ("" = the global default, matched against rows with no override).
	// Used by acl.GenerateACLForPlane to build a policy that only
	// includes identities on that plane — headscale rejects
	// unknown identities, so we can't mix plane A and plane B
	// identities in one policy.
	//
	// 2026-08-10: v0.33.1.41 — Issue 4 infra user. Filter
	// out rows with NULL headscale_user_id: the V054 migration
	// creates the 'infra' portal_users row at startup BEFORE
	// the headscale user is provisioned (ensureInfraUser
	// runs after V054 and may fail if headscale is briefly
	// unreachable). Including such a row in the ACL would
	// add `infra@<baseDomain>` as an identity that headscale
	// doesn't have, and the policy apply would fail with
	// "user not found". The filter makes the ACL builder
	// skip the row until ensureInfraUser links it on a later
	// tick — first apply after the link goes through picks
	// it up automatically. Same logic applies to any future
	// system users (e.g. a "service-bot" account).
	qSelectPortalUsernamesForPlane = `SELECT username FROM portal_users WHERE headscale_user_id IS NOT NULL AND (headscale_url = $1 OR (headscale_url = '' AND $2 = '')) ORDER BY id`
	// 2026-07-17: v0.17.0 — per-user subnet CIDR. Joins
	// portal_users (for username + plane) with user_subnets
	// (for the per-user 10.0.<uid>.0/24 CIDR). LEFT JOIN
	// because most users don't have a subnet allocated
	// yet — we just want the cidr (NULL/empty if absent).
	// The ACL builder uses this to extend the per-user
	// `dst: [user:*]` rule to `dst: [user:*, 10.0.<uid>.0/24:*]`
	// for users with a subnet.
	qSelectUserSubnetsForPlane = `
		SELECT p.username, COALESCE(s.cidr, '')
		  FROM portal_users p
		  LEFT JOIN user_subnets s ON s.user_id = p.id
		 WHERE p.headscale_url = $1 OR (p.headscale_url = '' AND $2 = '')
		 ORDER BY p.id`
	// 2026-07-17: v0.17.1 — for each user on the plane,
	// return the list of (grantor, cidr) tuples that
	// the grantee is allowed to access. The ACL builder
	// in v0.17.0 reads this to extend each user's
	// per-user dst list with every grantor's CIDR.
	// Returns one row per (grantee, grantor) pair
	// (zero rows if the grantee has no shares — the
	// caller treats that as "no extra dst entries").
	// LEFT JOIN is NOT needed: a share row only
	// exists if the grantor has a subnet (Grant
	// pre-checks this), and we don't want to surface
	// shares whose grantor has since had their
	// subnet disabled. So inner join is the right
	// choice — the acl builder trusts that any
	// CIDR returned here is currently routable.
	qSelectSharedSubnetsForPlane = `
		SELECT p_grantee.username, p_grantor.username, s.cidr
		  FROM user_subnet_shares sh
		  JOIN user_subnets s ON s.user_id = sh.grantor_user_id
		  JOIN portal_users p_grantor ON p_grantor.id = sh.grantor_user_id
		  JOIN portal_users p_grantee ON p_grantee.id = sh.grantee_user_id
		 WHERE (p_grantor.headscale_url = $1 OR (p_grantor.headscale_url = '' AND $2 = ''))
		   AND (p_grantee.headscale_url = $3 OR (p_grantee.headscale_url = '' AND $4 = ''))
		 ORDER BY p_grantee.username, p_grantor.username`
	// v0.22.0 — mesh (shared network) membership
	// visibility. For every (member, other_member) pair
	// within an active mesh on the given plane, return
	// (member_username, other_member_username,
	// other_member_cidr). The ACL builder reads this
	// the same way it reads GetSharedSubnetsForPlane:
	// for each user U on the plane, extend U's
	// per-user dst list with the CIDR of every other
	// member of every active mesh U belongs to.
	//
	// Self-pairs (U, U, U.cidr) are filtered out
	// because the per-user rule already includes the
	// user's own CIDR (v0.17.0). The mesh membership
	// table has the (mesh_id, user_id) PK; the query
	// joins mesh_members twice (once for "self" =
	// the user, once for "other" = the other member)
	// and the meshes table for the active-status
	// filter. The s.cidr LEFT JOIN means a user
	// without an allocated subnet contributes no rows
	// to the dst extension (we can't grant access to
	// a CIDR that doesn't exist yet).
	//
	// The plane filter applies to BOTH sides of the
	// pair (both users must be on the same headscale
	// instance — multi-plane deploys only bridge
	// within a plane, matching the v0.17.1 share
	// semantics).
	qSelectMeshMembershipsForPlane = `
		SELECT p_self.username, p_other.username, COALESCE(s_other.cidr, '')
		  FROM mesh_members mm_self
		  JOIN mesh_members mm_other
		    ON mm_other.mesh_id = mm_self.mesh_id
		   AND mm_other.user_id != mm_self.user_id
		  JOIN meshes m ON m.id = mm_self.mesh_id
		  JOIN portal_users p_self  ON p_self.id  = mm_self.user_id
		  JOIN portal_users p_other ON p_other.id = mm_other.user_id
		  LEFT JOIN user_subnets s_other ON s_other.user_id = mm_other.user_id
		 WHERE m.status = 'active'
		   AND (p_self.headscale_url  = $1 OR (p_self.headscale_url  = '' AND $2 = ''))
		   AND (p_other.headscale_url = $3 OR (p_other.headscale_url = '' AND $4 = ''))
		 ORDER BY p_self.username, p_other.username`
	// v0.13.0 — list every distinct (url, api_key) plane with a user
	// count. Used by the per-plane ACL pipeline to iterate all
	// planes and push the right policy to each. Empty
	// headscale_url = the global default.
	qSelectControlPlanes = `SELECT headscale_url, COUNT(*) FROM portal_users GROUP BY headscale_url`
	qSelectUserByID        = `SELECT username, headscale_user_id FROM portal_users WHERE id = $1`
	qSelectUserNameByID    = `SELECT username FROM portal_users WHERE id = $1`
	qSelectUserHSByID      = `SELECT headscale_user_id, username FROM portal_users WHERE id = $1`
	qSelectPasswordHash    = `SELECT password_hash FROM portal_users WHERE id = $1`
	qSelectHSIDByID        = `SELECT headscale_user_id FROM portal_users WHERE id = $1`
	qInsertPortalUser      = `INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id) VALUES ($1, $2, $3, $4) RETURNING id`
	// qInsertPortalUserAdopt powers db.InsertPortalUserAdopt.
	// v1.4.0 B141: "Adopt as skygate user" button on /admin/users
	// HSOrphans list. The pre-B141 admin UI only DISPLAYED the
	// orphans list (users.go:79) — to adopt one the operator had
	// to run a manual SQL INSERT into portal_users with the
	// headscale_user_id, plus a separate API call to set the
	// password. B141 wraps that into a single button.
	//
	// Uses ON CONFLICT(username) DO NOTHING so two concurrent
	// adopt clicks on the same orphan produce exactly one row
	// (the second click gets 0 rows affected, the handler
	// returns a friendly "already adopted" flash instead of
	// an error). The (theme, font_family, font_scale,
	// selection_bg) columns are intentionally omitted from
	// the INSERT — the V057 migration's column defaults
	// ('linear' / 'manrope' / 0 / '') apply on insert.
	qInsertPortalUserAdopt = `INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id) VALUES ($1, $2, 0, $3) ON CONFLICT(username) DO NOTHING RETURNING id`
	qUpdatePasswordHash    = `UPDATE portal_users SET password_hash = $1 WHERE id = $2`
	qDeletePortalUserByID  = `DELETE FROM portal_users WHERE id = $1`
)

// qSelectOtherHSUserIDs returns the headscale_user_id values of every
// portal user EXCEPT the one whose id matches `?`. Used by
// backfillNodeOwnership's Strategy A to short-circuit a node already
// claimed by a different portal user.
const qSelectOtherHSUserIDs = `SELECT headscale_user_id FROM portal_users WHERE id != $1 AND headscale_user_id IS NOT NULL AND headscale_user_id != ''`

// ---------------------------------------------------------------
// devices  —  v0.25 migration
//   id                INTEGER PRIMARY KEY AUTOINCREMENT
//   user_id           INTEGER NOT NULL
//   hostname          TEXT NOT NULL
//   node_id           TEXT DEFAULT ''
//   headscale_node_id TEXT DEFAULT ''
//   ip_addresses      TEXT DEFAULT ''
//   os                TEXT DEFAULT ''
//   last_seen         INTEGER DEFAULT 0
//   online            INTEGER DEFAULT 0
//   created_at        INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	// QSelectUserDevices is the user-scoped device list. Exported
	// (uppercase Q) so handlers can still use the raw constant
	// when they need the underlying *sql.Rows for App-level
	// enrichment (e.g. fall back to headscale.NodeList when the
	// devices table is empty).
	QSelectUserDevices = `SELECT id, hostname, last_seen FROM devices WHERE user_id = $1 ORDER BY hostname`
)

// ---------------------------------------------------------------
// device_rules  —  v0.20 + v0.21 + v0.22 + v0.25
//   id              INTEGER PRIMARY KEY AUTOINCREMENT
//   user_id         INTEGER NOT NULL
//   device_id       INTEGER NOT NULL
//   exit_node_id    TEXT NOT NULL
//   target_type     TEXT NOT NULL DEFAULT 'domain'  ('ip'|'subnet'|'domain')
//   target_value    TEXT NOT NULL
//   action          TEXT NOT NULL DEFAULT 'accept'  ('accept'|'deny')  v0.21
//   device_ip       TEXT NOT NULL DEFAULT ''                          v0.22
//   parent_domain   TEXT NOT NULL DEFAULT ''                          v0.25
//   enabled         INTEGER DEFAULT 1
//   created_at      INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	qCountAllEnabledRules     = `SELECT COUNT(*) FROM device_rules WHERE enabled = 1`
	qCountDeviceEnabledRules  = `SELECT COUNT(*) FROM device_rules WHERE device_id = $1 AND enabled = 1`
	qDistinctEnabledExitNodes = `SELECT DISTINCT exit_node_id FROM device_rules WHERE enabled = 1 AND exit_node_id != ''`
	qCountRulesByExitNode     = `SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled = 1 AND exit_node_id != '' GROUP BY exit_node_id`
	qCountRulesForExitNode    = `SELECT COUNT(*) FROM device_rules WHERE enabled = 1 AND exit_node_id = $1`
)

// qSelectEnabledACLEntries is used by GenerateACL to walk every rule and
// build the per-device HuJSON entries. v0.28.0: also returns
// user_name + device_hostname so the ACL builder can render
// src as tag:dev-<user>-<device> (preferred) instead of
// src = device_ip (fallback for pre-v0.28.0 rows where the
// backfill left the new columns empty).
const qSelectEnabledACLEntries = `SELECT target_type, target_value, action, COALESCE(device_ip, '') AS device_ip, COALESCE(user_name, '') AS user_name, COALESCE(device_hostname, '') AS device_hostname FROM device_rules WHERE enabled = 1`

// qSelectEnabledDomainRules is used by the autoupdater (resolves DNS → /32
// and inserts derived rules).
const qSelectEnabledDomainRules = `SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip, '') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'`

// qSelectEnabledSubnetIPRules powers the per-exit-node "available routes"
// list (the autoupdater fetches what's already enforced).
const qSelectEnabledSubnetIPRules = `SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id`

// qSelectSubnetRoutesForExitNode is used by the route-setup script
// generator to enumerate per-exit-node subnets and IPs.
const qSelectSubnetRoutesForExitNode = `SELECT target_value FROM device_rules WHERE enabled = 1 AND exit_node_id = $1 AND target_type IN ('subnet', 'ip')`

// qDeleteRuleByID removes a single rule. Cascading to derived /32 entries
// is the caller's job (see exit_rules_form_my.go PostDeleteExitRule).
const qDeleteRuleByID = `DELETE FROM device_rules WHERE id = $1`

// qDeleteRulesByIDOrParentDomain is the cascade used by the delete flow
// when deleting a domain rule: also drop any /32 entries that have
// parent_domain = ?.
const qDeleteRulesByIDOrParentDomain = `DELETE FROM device_rules WHERE user_id = $1 AND (id = $2 OR (target_type = 'subnet' AND parent_domain = $3))`

// qDeleteRulesByIDAndUser is the safe-by-ownership single delete.
const qDeleteRulesByIDAndUser = `DELETE FROM device_rules WHERE id = $1 AND user_id = $2`

// qCountEnabledUserRulesNonSubnet is used by the per-user quota panel
// (counts the "logical" rules, treating parent_domain IS NOT NULL /32
// rules as already-counted under their parent domain).
const qCountEnabledUserRulesNonSubnet = `SELECT COUNT(*) FROM device_rules WHERE user_id = $1 AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '')`

// qCountUserRulesWithExistingDomain is used by insertRuleUnique to check
// whether a duplicate (user, device, exit_node, domain) already exists.
const qSelectRuleByComposite = `SELECT id FROM device_rules WHERE user_id = $1 AND device_id = $2 AND exit_node_id = $3 AND target_type = $4 AND target_value = $5 LIMIT 1`

// qSelectExistingDomainForUpdate reads parent_domain from a row before
// update (used by the insert form to decide whether to insert or upsert).
const qSelectParentDomainByID = `SELECT COALESCE(parent_domain, '') FROM device_rules WHERE id = $1`

// qSelectDomainRuleForInsertDedup checks for an existing domain rule at
// (user, device, exit_node, domain) before a new insert.
const qSelectDomainRuleForInsertDedup = `SELECT id FROM device_rules WHERE user_id = $1 AND device_id = $2 AND exit_node_id = $3 AND target_type = 'domain' AND target_value = $4 LIMIT 1`

// qSelectSubnet32ForDomain finds existing /32 rows derived from a domain
// (used by both the delete cascade and the autoupdater).
const qSelectSubnet32ForDomain = `SELECT id, target_value FROM device_rules WHERE user_id = $1 AND device_id = $2 AND exit_node_id = $3 AND target_type = 'subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain, '') = $4`

// qSelectSubnet32NoParentDomain is the pre-cascade variant: /32 entries
// without a parent_domain, but for the same (user, device, exit_node) tuple.
const qSelectSubnet32NoParentDomain = `SELECT id, target_value FROM device_rules WHERE user_id = $1 AND device_id = $2 AND exit_node_id = $3 AND target_type = 'subnet' AND target_value LIKE '%/32'`

// qInsertDeviceRule is the canonical INSERT for a new rule. Action and
// parent_domain are caller-supplied (caller picks 'accept'/'deny' and
// whether to record a parent_domain link).
//
// 2026-08-17 (B125): ON CONFLICT clause closes the
// SELECT-then-INSERT race that previously let duplicate rows
// accumulate (Goal 37 found 114 redundant rules). The conflict
// target matches the UNIQUE INDEX device_rules_natural_key_uniq
// from migrateV056PG. DO UPDATE SET id = device_rules.id is a
// no-op that returns the EXISTING row's id when the conflict
// fires (PG's DO NOTHING can't RETURNING the old id) — so
// AppendDeviceRule is now a true "insert or get-existing" with
// no race window, returning the row's id in both cases.
const qInsertDeviceRule = `INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO UPDATE SET id = device_rules.id RETURNING id`

// qSelectUserRulesForView is used by /my/exit-rules: every enabled rule
// for a user, ordered for stable display.
const qSelectUserRulesForView = `SELECT d.id, d.user_id, d.device_id, d.exit_node_id, d.target_type, d.target_value, COALESCE(d.action, 'accept') AS action, COALESCE(d.device_ip, '') AS device_ip, d.enabled, COALESCE(d.parent_domain, '') AS parent_domain FROM device_rules d WHERE d.user_id = $1 ORDER BY d.id`

// qSelectAllRulesForAdmin is the cross-user admin view; LEFT JOIN onto
// portal_users so the row carries username even if the user was deleted.
const qSelectAllRulesForAdmin = `SELECT r.id, r.user_id, r.device_id, r.exit_node_id, r.target_type, r.target_value, r.action, COALESCE(r.parent_domain, ''), r.created_at, r.enabled, COALESCE(r.device_ip, '') AS device_ip, COALESCE(u.username, '?') AS user_name FROM device_rules r LEFT JOIN portal_users u ON u.id = r.user_id ORDER BY r.id`

// qSelectAllRulesForAdminByDevice is the cross-user admin view
// filtered to a single device hostname. The LEFT JOIN onto
// node_owner_map is what lets us filter by hostname (which is
// the user-facing identifier) instead of the headscale node_id
// (the DB key). Used by the per-device "dead rules" drill-down
// from /admin/devices.
//
// 2026-08-06: introduced for the /admin/exit-rules?device=NAME
// drill-down. The previous behavior was that the link from the
// per-device dead-rule count badge on /admin/devices pointed
// at /admin/exit-rules?device=NAME but the handler didn't filter
// — the operator saw all rules across all devices and had to
// scroll. Now the handler respects the query string.
//
// The hostname match is case-insensitive (LOWER on both sides)
// because /admin/devices stores hostnames in lowercase (see
// backfillNodeOwnership in internal/nodeownership), but a hand-
// edited `?device=` URL parameter could be any case.
const qSelectAllRulesForAdminByDevice = `SELECT r.id, r.user_id, r.device_id, r.exit_node_id, r.target_type, r.target_value, r.action, COALESCE(r.parent_domain, ''), r.created_at, r.enabled, COALESCE(r.device_ip, '') AS device_ip, COALESCE(u.username, '?') AS user_name FROM device_rules r LEFT JOIN portal_users u ON u.id = r.user_id LEFT JOIN node_owner_map n ON CAST(n.node_id AS INTEGER) = r.device_id WHERE LOWER(COALESCE(n.hostname, '')) = LOWER($1) ORDER BY r.id`

// qSelectTargetTypeByIDForDelete reads (target_type, parent_domain) of a
// single rule; the delete handler uses it to decide between single-row
// delete and cascade.
const qSelectTargetTypeByIDForDelete = `SELECT target_type, COALESCE(parent_domain, '') FROM device_rules WHERE id = $1 AND user_id = $2`

// qCountRulesByUserDeviceEnabled is the "this user has too many rules on
// this device" guard used by insertRuleUnique.
const qCountRulesByUserDeviceEnabled = `SELECT COUNT(*) FROM device_rules WHERE user_id = $1 AND device_id = $2 AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '')`

// qCountRulesByDeviceGrouped is the per-device count used by the
// /my/exit-rules usage panel (one row per device).
const qCountRulesByDeviceGrouped = `SELECT device_id, COUNT(*) FROM device_rules WHERE user_id = $1 AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '') GROUP BY device_id`

// ---------------------------------------------------------------
// preauth_keys  —  v0.25 migration
//   id                   INTEGER PRIMARY KEY AUTOINCREMENT
//   user_id              INTEGER NOT NULL
//   key                  TEXT NOT NULL UNIQUE
//   headscale_preauth_id TEXT DEFAULT ''
//   reusable             INTEGER NOT NULL DEFAULT 0
//   used                 INTEGER NOT NULL DEFAULT 0
//   expires_at           INTEGER DEFAULT 0
//   created_at           INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	qSelectPreauthByUser         = `SELECT id, COALESCE(headscale_preauth_id, ''), used, COALESCE(expires_at, 0) FROM preauth_keys WHERE user_id = $1`
	qSelectPreauthByUserDetailed = `SELECT id, key, used, COALESCE(expires_at, 0), created_at, COALESCE(headscale_preauth_id, '') FROM preauth_keys WHERE user_id = $1 ORDER BY created_at DESC`
	qSelectPreauthByID           = `SELECT used, COALESCE(expires_at, 0), COALESCE(headscale_preauth_id, '') FROM preauth_keys WHERE id = $1 AND user_id = $2`
	// qSelectPreauthFullByID returns every column for a single row
	// scoped to (id, user_id). Used by GetPreauthKeyByID for the
	// /my/keys/{id}/expire flow which needs headscale_preauth_id
	// to call headscale.ExpirePreauthKey. qSelectPreauthByID is
	// the legacy 3-column variant kept for any future lightweight
	// callers.
	//
	// 2026-07-11: COALESCE wraps the two nullable columns
	// (headscale_preauth_id, expires_at) so the helper can scan
	// into plain string / int64. The live DB schema (legacy
	// bootstrap, not v0.25's CREATE) declares both columns as
	// nullable; COALESCE normalizes NULL → '' / 0 and lets the
	// single helper serve both fresh DBs (NOT NULL DEFAULT) and
	// the live install.
	qSelectPreauthFullByID       = `SELECT id, user_id, key, COALESCE(headscale_preauth_id, ''), used, COALESCE(expires_at, 0), created_at FROM preauth_keys WHERE id = $1 AND user_id = $2`
	qInsertPreauthKey            = `INSERT INTO preauth_keys (user_id, key, expires_at, headscale_preauth_id) VALUES ($1, $2, $3, $4) RETURNING id`
	qSelectExpiringPreauthKeys   = `SELECT id, user_id, key, headscale_preauth_id, expires_at, created_at, reusable, used FROM preauth_keys WHERE used = 0 AND expires_at > 0 AND expires_at <= $1 ORDER BY expires_at ASC`
	qMarkPreauthKeyNotified      = `UPDATE preauth_keys SET notified_at = $2 WHERE id = $1`
	qResetPreauthKeyNotified     = `UPDATE preauth_keys SET notified_at = 0 WHERE id = $1`
	qInsertNotification           = `INSERT INTO notifications (user_id, type, severity, title, body, link, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	qListNotificationsByUser     = `SELECT id, user_id, type, severity, title, body, link, created_at, read_at FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`
	qListUnreadNotificationsByUser = `SELECT id, user_id, type, severity, title, body, link, created_at, read_at FROM notifications WHERE user_id = $1 AND read_at = 0 ORDER BY created_at DESC LIMIT $2`
	qCountUnreadNotifications    = `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND read_at = 0`
	qMarkNotificationRead        = `UPDATE notifications SET read_at = $1 WHERE id = $2 AND user_id = $3 AND read_at = 0`
	qMarkAllNotificationsRead    = `UPDATE notifications SET read_at = $1 WHERE user_id = $2 AND read_at = 0`
	qDeleteNotificationsForUser  = `DELETE FROM notifications WHERE user_id = $1`
	qUpdatePreauthExpires        = `UPDATE preauth_keys SET expires_at = $1 WHERE id = $2 AND user_id = $3`
	qMarkPreauthUsed             = `UPDATE preauth_keys SET used = 1 WHERE headscale_preauth_id = $1 AND used = 0`
	qDeletePreauthByUser         = `DELETE FROM preauth_keys WHERE user_id = $1`
)

// ---------------------------------------------------------------
// node_owner_map  —  v0.25 migration (originally; later v0.28 widened
//                   with tag/tagged_by/tagged_at columns — see migrations)
//   node_id         TEXT PRIMARY KEY
//   user_id         INTEGER NOT NULL
//   username        TEXT DEFAULT ''
//   attributed_at   INTEGER DEFAULT (strftime('%s','now'))
//   tag             TEXT DEFAULT ''                  -- tag:private | tag:public | ...
//   tagged_by_user_id INTEGER DEFAULT 0
//   tagged_at       INTEGER DEFAULT 0
// ---------------------------------------------------------------

const (
	qSelectNodeOwnerByUsername  = `SELECT node_id FROM node_owner_map WHERE username = $1`
	qSelectNodeOwnerByNodeID    = `SELECT node_id FROM node_owner_map WHERE node_id = $1 AND username = $2`
	qDeleteNodeOwnerByID        = `DELETE FROM node_owner_map WHERE node_id = $1 AND username = $2`
	qDeleteNodeOwnerByNodeTag   = `DELETE FROM node_owner_map WHERE node_id = $1 AND tag = $2`
	qCountNodeOwnerByNodeUser   = `SELECT COUNT(*) FROM node_owner_map WHERE node_id = $1 AND username = $2`
	qInsertOrReplaceNodeOwner   = `INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at) VALUES ($1, $2, $3, $4, $5, ` + nowUnix + `) ON CONFLICT(node_id) DO UPDATE SET headscale_user_id = excluded.headscale_user_id, username = excluded.username, tag = excluded.tag, tagged_by_user_id = excluded.tagged_by_user_id, tagged_at = excluded.tagged_at`
	qUpdateNodeOwnerTag         = `UPDATE node_owner_map SET tag = $1, tagged_by_user_id = $2, tagged_at = ` + nowUnix + ` WHERE node_id = $3 AND username = $4`
)

// ---------------------------------------------------------------
// personal_api_tokens  —  v0.23 migration
//   id            INTEGER PRIMARY KEY AUTOINCREMENT
//   user_id       INTEGER NOT NULL
//   token_hash    TEXT NOT NULL UNIQUE
//   label         TEXT NOT NULL DEFAULT ''
//   last_used_at  INTEGER DEFAULT 0
//   created_at    INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	qSelectAllAPITokensForLookup = `SELECT pt.user_id, pu.username, pu.is_admin, pt.token_hash, pt.expires_at FROM personal_api_tokens pt JOIN portal_users pu ON pu.id = pt.user_id`
	qSelectAPITokensByUser       = `SELECT id, label, last_used_at, created_at, expires_at, auto_rotate FROM personal_api_tokens WHERE user_id = $1 ORDER BY created_at DESC`
	qInsertAPIToken              = `INSERT INTO personal_api_tokens (user_id, token_hash, label, expires_at, auto_rotate) VALUES ($1, $2, $3, $4, $5) RETURNING id`
	qDeleteAPITokenByUser        = `DELETE FROM personal_api_tokens WHERE id = $1 AND user_id = $2`
	qUpdateAPITokenExpiryByUser  = `UPDATE personal_api_tokens SET expires_at = $3 WHERE id = $1 AND user_id = $2`
	qUpdateAPITokenExpiryByID    = `UPDATE personal_api_tokens SET expires_at = $2 WHERE id = $1`
	qSelectAPITokensForAutoRotate = `SELECT id, user_id, label, expires_at FROM personal_api_tokens WHERE auto_rotate = 1 AND expires_at > 0 AND expires_at <= $1 ORDER BY expires_at ASC`
	qDeleteAPITokensByUserID     = `DELETE FROM personal_api_tokens WHERE user_id = $1`
	qTouchAPITokenLastUsed       = `UPDATE personal_api_tokens SET last_used_at = strftime('%s', 'now') WHERE token_hash = $1`
)

// ---------------------------------------------------------------
// telegram_bindings  —  v0.29 migration
//   chat_id           INTEGER PRIMARY KEY
//   portal_user_id    INTEGER NOT NULL
//   is_admin          INTEGER NOT NULL DEFAULT 0
//   bound_at          INTEGER NOT NULL DEFAULT (strftime('%s','now'))
//   bound_by_user_id  INTEGER NOT NULL DEFAULT 0
// ---------------------------------------------------------------

const (
	qSelectTelegramBindingByChatID = `SELECT chat_id, portal_user_id, is_admin, bound_at, bound_by_user_id, lang FROM telegram_bindings WHERE chat_id = $1`
	qSelectTelegramBindingByUser   = `SELECT chat_id, portal_user_id, is_admin, bound_at, bound_by_user_id, lang FROM telegram_bindings WHERE portal_user_id = $1`
	qSelectAllTelegramBindings     = `SELECT chat_id, portal_user_id, is_admin, bound_at, bound_by_user_id, lang FROM telegram_bindings ORDER BY bound_at DESC`
	// Этап 14 v5: lang is set ONLY on the INSERT branch of the
	// upsert. A re-bind (admin /bind rebinds an existing chat
	// to a different user) must NOT overwrite the lang the
	// user explicitly chose with /lang. The lang column still
	// appears in the INSERT for fresh binds (so auto-detect at
	// /login writes the right value the first time).
	qInsertTelegramBinding         = `INSERT INTO telegram_bindings (chat_id, portal_user_id, is_admin, bound_by_user_id, lang) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(chat_id) DO UPDATE SET portal_user_id = excluded.portal_user_id, is_admin = excluded.is_admin, bound_at = strftime('%s','now'), bound_by_user_id = excluded.bound_by_user_id`
	qUpdateTelegramBindingLang     = `UPDATE telegram_bindings SET lang = $1 WHERE chat_id = $2`
	qDeleteTelegramBindingByChat   = `DELETE FROM telegram_bindings WHERE chat_id = $1`
	qDeleteTelegramBindingsByUser  = `DELETE FROM telegram_bindings WHERE portal_user_id = $1`
)

// ---------------------------------------------------------------
// telegram_login_tokens  —  v0.31
//   token            TEXT PRIMARY KEY
//   portal_user_id   INTEGER NOT NULL
//   created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now'))
//   expires_at       INTEGER NOT NULL
//   used_at          INTEGER NOT NULL DEFAULT 0
//   used_by_chat_id  INTEGER NOT NULL DEFAULT 0
//   request_ip       TEXT    NOT NULL DEFAULT ''
//
// Этап 12 (2026-07-13): login-by-key. User generates a one-time
// token in /my/telegram, pastes it into the bot via /login, the bot
// UPSERTs the binding and marks the token used. Strict-mode gate
// in HandleCommand requires a binding row before letting the chat
// touch any portal data.
// ---------------------------------------------------------------

const (
	qInsertTelegramLoginToken = `INSERT INTO telegram_login_tokens
		(token, portal_user_id, created_at, expires_at, used_at, used_by_chat_id, request_ip)
		VALUES ($1, $2, strftime('%s','now'), $3, 0, 0, $4)`
	qSelectTelegramLoginToken = `SELECT token, portal_user_id, created_at, expires_at, used_at, used_by_chat_id, request_ip
		FROM telegram_login_tokens WHERE token = $1`
	qConsumeTelegramLoginToken = `UPDATE telegram_login_tokens
		SET used_at = strftime('%s','now'),
		    used_by_chat_id = $1
		WHERE token = $2 AND used_at = 0`
	qDeleteTelegramLoginToken         = `DELETE FROM telegram_login_tokens WHERE token = $1`
	qDeleteExpiredTelegramLoginTokens = `DELETE FROM telegram_login_tokens WHERE expires_at < $1`
	qDeleteTelegramLoginTokensByUser  = `DELETE FROM telegram_login_tokens WHERE portal_user_id = $1`
	qCountActiveTelegramLoginTokensByUser = `SELECT COUNT(*) FROM telegram_login_tokens
		WHERE portal_user_id = $1 AND used_at = 0 AND expires_at > strftime('%s','now')`
	qListTelegramLoginTokensByUser = `SELECT token, portal_user_id, created_at, expires_at, used_at, used_by_chat_id, request_ip
		FROM telegram_login_tokens WHERE portal_user_id = $1
		ORDER BY created_at DESC LIMIT $2`
)

// ---------------------------------------------------------------
// telegram_rate_limit  —  v0.32
//   key     TEXT NOT NULL   "<scope>:<id>", e.g. "login:555"
//   action  TEXT NOT NULL DEFAULT ''  (reserved for future use)
//   ts      INTEGER NOT NULL  unix seconds
//
// Этап 13 (2026-07-13): replaces the in-memory loginAttempts
// map in internal/telegram. Atomic per attempt: one INSERT,
// one SELECT (count rows in the window). Survives restarts
// and works across instances.
// ---------------------------------------------------------------

const (
	qInsertTelegramRateLimit = `INSERT INTO telegram_rate_limit(key, action, ts)
		VALUES ($1, $2, $3)`
	qCountTelegramRateLimitInWindow = `SELECT COUNT(*) FROM telegram_rate_limit
		WHERE key = $1 AND ts >= $2`
	qDeleteTelegramRateLimitOlderThan = `DELETE FROM telegram_rate_limit WHERE ts < $1`
)

// ---------------------------------------------------------------
// exit_servers  —  v0.20 + v0.24
//   id                INTEGER PRIMARY KEY AUTOINCREMENT
//   node_id           TEXT NOT NULL UNIQUE
//   hostname          TEXT NOT NULL
//   tailscale_ip      TEXT NOT NULL DEFAULT ''
//   ssh_target        TEXT NOT NULL DEFAULT ''             v0.24
//   ssh_key_path      TEXT NOT NULL DEFAULT ''             v0.24
//   description       TEXT DEFAULT ''
//   accept_routes     INTEGER DEFAULT 1
//   enabled           INTEGER DEFAULT 1
//   created_at        INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	// qSelectAllExitServers is the row shape used by db.ListExitServers.
	// v0.33.1.33 B85: also returns ssh_port (the per-row non-default
	// SSH port for the B81 auto-fallback) — see the ExitServer.SSHPort
	// field comment for the contract.
	qSelectAllExitServers         = `SELECT id, node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, COALESCE(ssh_port, ''), enabled, COALESCE(description, ''), accept_routes FROM exit_servers ORDER BY hostname`
	// qSelectAcceptRoutesByHost powers db.LookupExitServerAcceptRoutes.
	qSelectAcceptRoutesByHost     = `SELECT accept_routes FROM exit_servers WHERE hostname = $1 LIMIT 1`
	// qSelectExitServerSSH powers db.LookupExitServerSSH. Returns the
	// per-row ssh_target + ssh_key_path so the v0.33.1 SetAdvertisedRoutes
	// call can SSH to the right host:port as the right user with the right
	// key (the previous hard-coded `-F /home/admin/.ssh/config` only worked
	// for one operator on one machine).
	qSelectExitServerSSH         = `SELECT COALESCE(ssh_target, ''), COALESCE(ssh_key_path, '') FROM exit_servers WHERE hostname = $1 LIMIT 1`
	// qSelectExitServerSSHTarget powers db.LookupExitServerSSHTarget.
	// v0.33.1.29 B81: returns BOTH ssh_target AND tailscale_ip so the
	// helper can implement the fallback chain in Go (the chain is
	// stored ssh_target → "root@<tailscale_ip>" → ""). v0.33.1.33
	// B85: also returns ssh_port so the B81 auto-fallback can
	// append a ":<port>" suffix when the operator has set a
	// non-default SSH port on the exit-node (the exit-node may
	// not be running sshd on 22, e.g. moved to 2222 for security).
	qSelectExitServerSSHTarget   = `SELECT COALESCE(ssh_target, ''), COALESCE(tailscale_ip, ''), COALESCE(ssh_port, '') FROM exit_servers WHERE hostname = $1 LIMIT 1`
	// qInsertOrReplaceExitServer powers db.UpsertExitServer.
	// v0.33.1.33 B85: also writes ssh_port so the B81 auto-fallback
	// can append ":<port>" to "root@<tailscale_ip>". The COALESCE
	// in the ON CONFLICT branch isn't needed (excluded.ssh_port is
	// the value we just inserted, never NULL), but keeping the
	// form consistent with the row read.
	qInsertOrReplaceExitServer    = `INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, ssh_port, accept_routes) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT(node_id) DO UPDATE SET hostname = excluded.hostname, ssh_target = excluded.ssh_target, ssh_key_path = excluded.ssh_key_path, description = excluded.description, ssh_port = excluded.ssh_port, accept_routes = excluded.accept_routes`
	// qDeleteExitServerByNodeID powers db.DeleteExitServerByNodeID.
	qDeleteExitServerByNodeID     = `DELETE FROM exit_servers WHERE node_id = $1`
	// qInsertExitServerOnDiscovery powers db.InsertIgnoreExitServerOnDiscovery.
	qInsertExitServerOnDiscovery  = `INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES ($1, $2, $3) ON CONFLICT(node_id) DO NOTHING`
	// qUpdateExitServerAcceptRoutes powers db.SetExitServerAcceptRoutes.
	// v1.4.0 B140: per-row accept_routes toggle on /admin/exit-nodes.
	// Updates just the accept_routes column (1=true, -1=false, 0=default)
	// without touching any other column. The pre-B140 admin UI had no
	// way to change this value after the initial node add — the form
	// in PostAdminExitNodesAdd was the only entry point, and editing
	// any other field via the row's actions column would NOT update
	// accept_routes (UpsertExitServer would, but the per-row buttons
	// don't call it for the accept_routes use case).
	qUpdateExitServerAcceptRoutes = `UPDATE exit_servers SET accept_routes = $1 WHERE node_id = $2`
	// qSelectExitServerNodeIDByNodeID powers db.GetExitServerNodeID.
	// Returns the node_id for a given node_id (the lookup is used
	// to validate the row exists before attempting an UPDATE — gives
	// a clear 404 error instead of a silent no-op on missing rows).
	qSelectExitServerByNodeID      = `SELECT COALESCE(hostname, '') FROM exit_servers WHERE node_id = $1 LIMIT 1`
)

// ---------------------------------------------------------------
// global_settings  —  v0.21 migration
//   key          TEXT PRIMARY KEY
//   value        TEXT NOT NULL DEFAULT ''
//   updated_at   INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------


// ---------------------------------------------------------------
// node_owner_map JOIN portal_users  —  v0.28.0 per-device ACL tag
//   tag         TEXT  format: "tag:dev-<username>-<hostname>"
// ---------------------------------------------------------------

// v0.28.0: query returns the per-device ACL tag for every
// row in node_owner_map joined with portal_users. planeURL
// filter on the user side is a TODO for the per-plane
// refactor (currently every device lives in the global
// headscale, so the filter is a no-op for production).
// Argument list: 4 × planeURL (one for the per-user subquery,
// one for shared/mesh — kept for parity with the other
// per-plane helpers; not used yet).
const qSelectPerUserDeviceTags = `SELECT pu.username, nom.hostname, 'tag:dev-' || pu.username || '-' || nom.hostname AS tag
  FROM node_owner_map nom
  JOIN portal_users pu ON nom.username = pu.username
 WHERE nom.hostname != ''
   AND nom.username != ''
 ORDER BY pu.username, nom.hostname`
