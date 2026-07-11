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
	qInsertExitRuleLog = `INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, ?, ?)`
)

// qSelectLastSyncForExitNode returns the most recent sync log line that
// mentions `?` (an exit_node name) in its detail. Used by the admin node
// load dashboard to show "last sync" per exit-node. The detail column is
// free-form text so we LIKE-match.
const qSelectLastSyncForExitNode = `SELECT COALESCE(MAX(created_at), 0) FROM exit_rule_logs WHERE action = 'sync' AND detail LIKE ?`

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
	qInsertACLSnapshot = `INSERT INTO acl_snapshots (version, config, created_by, applied_success) VALUES (?, ?, ?, 1)`

	// qMarkACLApplied is fired after headscale has accepted the policy.
	qMarkACLApplied = `UPDATE acl_snapshots SET applied_success = 1 WHERE version = ?`

	// qMarkACLFail records a failure reason. error_msg must be non-NULL
	// (the column allows TEXT but headscale error strings can be long).
	qMarkACLFail = `UPDATE acl_snapshots SET applied_success = 0, error_msg = ? WHERE version = ?`

	// qSelectACLConfig reads the HuJSON policy BLOB for a given version.
	// Rollback handlers feed this back into headscale.
	qSelectACLConfig = `SELECT config FROM acl_snapshots WHERE version = ?`

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
	qInsertAuditLog = `INSERT INTO audit_log (user_id, username, action, detail) VALUES (?, ?, ?, ?)`

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
	qSelectUserByName      = `SELECT id, password_hash, is_admin FROM portal_users WHERE username = ?`
	qSelectUserIDByName    = `SELECT id FROM portal_users WHERE username = ?`
	qSelectAllPortalUsers  = `SELECT id, username, is_admin, headscale_user_id, created_at, theme FROM portal_users ORDER BY id`
	qSelectPortalUsernames = `SELECT username FROM portal_users ORDER BY id`
	qSelectUserByID        = `SELECT username, headscale_user_id FROM portal_users WHERE id = ?`
	qSelectUserNameByID    = `SELECT username FROM portal_users WHERE id = ?`
	qSelectUserHSByID      = `SELECT headscale_user_id, username FROM portal_users WHERE id = ?`
	qSelectPasswordHash    = `SELECT password_hash FROM portal_users WHERE id = ?`
	qSelectHSIDByID        = `SELECT headscale_user_id FROM portal_users WHERE id = ?`
	qInsertPortalUser      = `INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id) VALUES (?, ?, ?, ?)`
	qUpdatePasswordHash    = `UPDATE portal_users SET password_hash = ? WHERE id = ?`
	qDeletePortalUserByID  = `DELETE FROM portal_users WHERE id = ?`
)

// qSelectOtherHSUserIDs returns the headscale_user_id values of every
// portal user EXCEPT the one whose id matches `?`. Used by
// backfillNodeOwnership's Strategy A to short-circuit a node already
// claimed by a different portal user.
const qSelectOtherHSUserIDs = `SELECT headscale_user_id FROM portal_users WHERE id != ? AND headscale_user_id IS NOT NULL AND headscale_user_id != ''`

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
	qSelectUserDevices = `SELECT id, hostname, last_seen FROM devices WHERE user_id = ? ORDER BY hostname`
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
	qCountDeviceEnabledRules  = `SELECT COUNT(*) FROM device_rules WHERE device_id = ? AND enabled = 1`
	qDistinctEnabledExitNodes = `SELECT DISTINCT exit_node_id FROM device_rules WHERE enabled = 1 AND exit_node_id != ''`
	qCountRulesByExitNode     = `SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled = 1 AND exit_node_id != '' GROUP BY exit_node_id`
	qCountRulesForExitNode    = `SELECT COUNT(*) FROM device_rules WHERE enabled = 1 AND exit_node_id = ?`
)

// qSelectEnabledACLEntries is used by GenerateACL to walk every rule and
// build the per-device HuJSON entries.
const qSelectEnabledACLEntries = `SELECT target_type, target_value, action, COALESCE(device_ip, '') AS device_ip FROM device_rules WHERE enabled = 1`

// qSelectEnabledDomainRules is used by the autoupdater (resolves DNS → /32
// and inserts derived rules).
const qSelectEnabledDomainRules = `SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip, '') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'`

// qSelectEnabledSubnetIPRules powers the per-exit-node "available routes"
// list (the autoupdater fetches what's already enforced).
const qSelectEnabledSubnetIPRules = `SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id`

// qSelectSubnetRoutesForExitNode is used by the route-setup script
// generator to enumerate per-exit-node subnets and IPs.
const qSelectSubnetRoutesForExitNode = `SELECT target_value FROM device_rules WHERE enabled = 1 AND exit_node_id = ? AND target_type IN ('subnet', 'ip')`

// qDeleteRuleByID removes a single rule. Cascading to derived /32 entries
// is the caller's job (see exit_rules_form_my.go PostDeleteExitRule).
const qDeleteRuleByID = `DELETE FROM device_rules WHERE id = ?`

// qDeleteRulesByIDOrParentDomain is the cascade used by the delete flow
// when deleting a domain rule: also drop any /32 entries that have
// parent_domain = ?.
const qDeleteRulesByIDOrParentDomain = `DELETE FROM device_rules WHERE user_id = ? AND (id = ? OR (target_type = 'subnet' AND parent_domain = ?))`

// qDeleteRulesByIDAndUser is the safe-by-ownership single delete.
const qDeleteRulesByIDAndUser = `DELETE FROM device_rules WHERE id = ? AND user_id = ?`

// qCountEnabledUserRulesNonSubnet is used by the per-user quota panel
// (counts the "logical" rules, treating parent_domain IS NOT NULL /32
// rules as already-counted under their parent domain).
const qCountEnabledUserRulesNonSubnet = `SELECT COUNT(*) FROM device_rules WHERE user_id = ? AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '')`

// qCountUserRulesWithExistingDomain is used by insertRuleUnique to check
// whether a duplicate (user, device, exit_node, domain) already exists.
const qSelectRuleByComposite = `SELECT id FROM device_rules WHERE user_id = ? AND device_id = ? AND exit_node_id = ? AND target_type = ? AND target_value = ? LIMIT 1`

// qSelectExistingDomainForUpdate reads parent_domain from a row before
// update (used by the insert form to decide whether to insert or upsert).
const qSelectParentDomainByID = `SELECT COALESCE(parent_domain, '') FROM device_rules WHERE id = ?`

// qSelectDomainRuleForInsertDedup checks for an existing domain rule at
// (user, device, exit_node, domain) before a new insert.
const qSelectDomainRuleForInsertDedup = `SELECT id FROM device_rules WHERE user_id = ? AND device_id = ? AND exit_node_id = ? AND target_type = 'domain' AND target_value = ? LIMIT 1`

// qSelectSubnet32ForDomain finds existing /32 rows derived from a domain
// (used by both the delete cascade and the autoupdater).
const qSelectSubnet32ForDomain = `SELECT id, target_value FROM device_rules WHERE user_id = ? AND device_id = ? AND exit_node_id = ? AND target_type = 'subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain, '') = ?`

// qSelectSubnet32NoParentDomain is the pre-cascade variant: /32 entries
// without a parent_domain, but for the same (user, device, exit_node) tuple.
const qSelectSubnet32NoParentDomain = `SELECT id, target_value FROM device_rules WHERE user_id = ? AND device_id = ? AND exit_node_id = ? AND target_type = 'subnet' AND target_value LIKE '%/32'`

// qInsertDeviceRule is the canonical INSERT for a new rule. Action and
// parent_domain are caller-supplied (caller picks 'accept'/'deny' and
// whether to record a parent_domain link).
const qInsertDeviceRule = `INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// qSelectUserRulesForView is used by /my/exit-rules: every enabled rule
// for a user, ordered for stable display.
const qSelectUserRulesForView = `SELECT d.id, d.user_id, d.device_id, d.exit_node_id, d.target_type, d.target_value, COALESCE(d.action, 'accept') AS action, COALESCE(d.device_ip, '') AS device_ip, d.enabled, COALESCE(d.parent_domain, '') AS parent_domain FROM device_rules d WHERE d.user_id = ? ORDER BY d.id`

// qSelectAllRulesForAdmin is the cross-user admin view; LEFT JOIN onto
// portal_users so the row carries username even if the user was deleted.
const qSelectAllRulesForAdmin = `SELECT r.id, r.user_id, r.device_id, r.exit_node_id, r.target_type, r.target_value, r.action, COALESCE(r.parent_domain, ''), r.created_at, r.enabled, COALESCE(r.device_ip, '') AS device_ip, COALESCE(u.username, '?') AS user_name FROM device_rules r LEFT JOIN portal_users u ON u.id = r.user_id ORDER BY r.id`

// qSelectTargetTypeByIDForDelete reads (target_type, parent_domain) of a
// single rule; the delete handler uses it to decide between single-row
// delete and cascade.
const qSelectTargetTypeByIDForDelete = `SELECT target_type, COALESCE(parent_domain, '') FROM device_rules WHERE id = ? AND user_id = ?`

// qCountRulesByUserDeviceEnabled is the "this user has too many rules on
// this device" guard used by insertRuleUnique.
const qCountRulesByUserDeviceEnabled = `SELECT COUNT(*) FROM device_rules WHERE user_id = ? AND device_id = ? AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '')`

// qCountRulesByDeviceGrouped is the per-device count used by the
// /my/exit-rules usage panel (one row per device).
const qCountRulesByDeviceGrouped = `SELECT device_id, COUNT(*) FROM device_rules WHERE user_id = ? AND enabled = 1 AND (target_type != 'subnet' OR COALESCE(parent_domain, '') = '') GROUP BY device_id`

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
	qSelectPreauthByUser         = `SELECT id, headscale_preauth_id, used, expires_at FROM preauth_keys WHERE user_id = ?`
	qSelectPreauthByUserDetailed = `SELECT id, key, used, expires_at, created_at, headscale_preauth_id FROM preauth_keys WHERE user_id = ? ORDER BY created_at DESC`
	qSelectPreauthByID           = `SELECT used, expires_at, headscale_preauth_id FROM preauth_keys WHERE id = ? AND user_id = ?`
	qInsertPreauthKey            = `INSERT INTO preauth_keys (user_id, key, expires_at, headscale_preauth_id) VALUES (?, ?, ?, ?)`
	qUpdatePreauthExpires        = `UPDATE preauth_keys SET expires_at = ? WHERE id = ? AND user_id = ?`
	qMarkPreauthUsed             = `UPDATE preauth_keys SET used = 1 WHERE headscale_preauth_id = ? AND used = 0`
	qDeletePreauthByUser         = `DELETE FROM preauth_keys WHERE user_id = ?`
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
	qSelectNodeOwnerByUsername  = `SELECT node_id FROM node_owner_map WHERE username = ?`
	qSelectNodeOwnerByNodeID    = `SELECT node_id FROM node_owner_map WHERE node_id = ? AND username = ?`
	qDeleteNodeOwnerByID        = `DELETE FROM node_owner_map WHERE node_id = ? AND username = ?`
	qDeleteNodeOwnerByNodeTag   = `DELETE FROM node_owner_map WHERE node_id = ? AND tag = ?`
	qCountNodeOwnerByNodeUser   = `SELECT COUNT(*) FROM node_owner_map WHERE node_id = ? AND username = ?`
	qInsertOrReplaceNodeOwner   = `INSERT OR REPLACE INTO node_owner_map (node_id, username, tag, tagged_by_user_id, tagged_at) VALUES (?, ?, ?, ?, strftime('%s', 'now'))`
	qUpdateNodeOwnerTag         = `UPDATE node_owner_map SET tag = ?, tagged_by_user_id = ?, tagged_at = strftime('%s', 'now') WHERE node_id = ? AND username = ?`
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
	qSelectAllAPITokensForLookup = `SELECT pt.user_id, pu.username, pu.is_admin, pt.token_hash FROM personal_api_tokens pt JOIN portal_users pu ON pu.id = pt.user_id`
	qSelectAPITokensByUser       = `SELECT id, label, last_used_at, created_at FROM personal_api_tokens WHERE user_id = ? ORDER BY created_at DESC`
	qInsertAPIToken              = `INSERT INTO personal_api_tokens (user_id, token_hash, label) VALUES (?, ?, ?)`
	qDeleteAPITokenByUser        = `DELETE FROM personal_api_tokens WHERE id = ? AND user_id = ?`
	qTouchAPITokenLastUsed       = `UPDATE personal_api_tokens SET last_used_at = strftime('%s', 'now') WHERE token_hash = ?`
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
	qSelectEnabledExitServers     = `SELECT name FROM exit_servers WHERE enabled = 1`                                 // legacy "name" column? — see note
	qSelectAllExitServers         = `SELECT id, node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, enabled, COALESCE(description, ''), accept_routes FROM exit_servers ORDER BY hostname`
	qSelectEnabledExitServerNames = `SELECT DISTINCT exit_node_id FROM device_rules WHERE enabled = 1 AND exit_node_id != ''`
	qSelectAcceptRoutesByHost     = `SELECT accept_routes FROM exit_servers WHERE hostname = ? LIMIT 1`
	qInsertOrReplaceExitServer    = `INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, accept_routes) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(node_id) DO UPDATE SET hostname = excluded.hostname, ssh_target = excluded.ssh_target, ssh_key_path = excluded.ssh_key_path, description = excluded.description, accept_routes = excluded.accept_routes`
	qDeleteExitServerByNodeID     = `DELETE FROM exit_servers WHERE node_id = ?`
	qInsertExitServerOnDiscovery  = `INSERT OR IGNORE INTO exit_servers (node_id, hostname, tailscale_ip) VALUES (?, ?, ?)`
)

// ---------------------------------------------------------------
// global_settings  —  v0.21 migration
//   key          TEXT PRIMARY KEY
//   value        TEXT NOT NULL DEFAULT ''
//   updated_at   INTEGER DEFAULT (strftime('%s','now'))
// ---------------------------------------------------------------

const (
	qSelectExitPolicy       = `SELECT value FROM global_settings WHERE key = 'exit_policy'`
	qUpsertExitPolicy       = `INSERT OR REPLACE INTO global_settings (key, value) VALUES ('exit_policy', ?)`
	qMaxTelegramSettingTime = `SELECT MAX(updated_at) FROM global_settings WHERE key IN (?, ?)`
)
