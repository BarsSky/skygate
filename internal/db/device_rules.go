package db

import (
	"database/sql"
	"errors"
	"strings"
)

// device_rules  —  helpers
//
// 2026-07-11: refactor v0.6.0 (Этап 9 part 2). Before this file the
// same SQL strings were duplicated across exit_rules.go, exit_rules_*.go,
// exit_rules_api.go, exit_rules_sync.go, and admin_exit_nodes.go. The
// most painful was the SELECT-then-INSERT dedup pattern in
// insertRuleUnique — three near-identical copies existed (exit_rules.go
// for the form, exit_rules_api.go for the API, exit_rules_sync.go for
// the autoupdater), and the strings drifted subtly.
//
// Helpers here are split into two camps:
//
//   1. Typed read/write helpers (AppendDeviceRule, FindDeviceRuleID,
//      CountEnabledRules, GetDeviceRulesForUser, etc.) — these return
//      Go structs or scalar values, NOT *sql.Rows. Callers should
//      prefer these.
//
//   2. Raw query constants (qInsertDeviceRule, qSelectUserRulesForView,
//      etc.) — already in queries.go. The helpers above use them; if
//      a caller needs a one-off shape (e.g. an unusual JOIN) it should
//      still reach for the constant rather than a string literal.

// DeviceRule mirrors one row of device_rules, plus a few derived
// fields that the /my/exit-rules view fills in from headscale
// (DeviceName) and from the LEFT JOIN in the admin view (UserName).
// The db package does not populate DeviceName or UserName — those
// require App-level headscale lookups or a JOIN, so the App methods
// are responsible for filling them in.
type DeviceRule struct {
	ID           int
	UserID       int
	UserName     string // only set by GetAllRulesForAdmin (LEFT JOIN)
	DeviceID     int
	DeviceName   string // only set by App.getDeviceRules (headscale lookup)
	ExitNodeID   string
	TargetType   string
	TargetValue  string
	Action       string
	DeviceIP     string
	Enabled      bool
	ParentDomain string
	CreatedAt    int64 // only set by GetAllRulesForAdmin (the JOINed row)
}

// ACLEntry is a slimmed-down view of DeviceRule used by GenerateACL.
// Only the four columns the ACL builder reads; saves copying the
// other 7 fields per row.
type ACLEntry struct {
	TargetType  string
	TargetValue string
	Action      string
	DeviceIP    string
}

// DomainRule is used by the autoupdater to walk enabled domain rules
// and resolve them to /32 entries.
type DomainRule struct {
	ID         int
	UserID     int
	DeviceID   int
	ExitNodeID string
	Target     string
	Action     string
	DeviceIP   string
}

// SubnetIPRule is a (exit_node, target) pair used by the autoupdater
// to build the per-exit-node route list. The DISTINCT in SQL drops
// duplicates that the (subnet,ip) target_type split could otherwise
// create for the same value.
type SubnetIPRule struct {
	ExitNodeID string
	Target     string
}

// CountRulesByDevice groups rules-per-device for a single user. The
// map key is device_id, the value is the count. Used by the
// /my/exit-rules usage panel.
type CountRulesByDevice = map[int]int

// CountRulesByExitNodeMap groups rule counts by exit_node_id. The
// map key is the exit_node name (string), the value is the count.
type CountRulesByExitNodeMap = map[string]int

// ErrNotFound is returned by FindDeviceRuleID / FindDomainRuleID
// when no matching row exists. Callers can use errors.Is to detect
// "no existing rule" and proceed with the insert path.
var ErrNotFound = errors.New("db: device_rule not found")

// AppendDeviceRule inserts one row into device_rules. The new row's
// id is returned. parent_domain is typically set to target_value for
// domain rules (so the autoupdater can track them) and "" otherwise.
// Callers wanting dedup should call FindDeviceRuleID first.
func AppendDeviceRule(d *sql.DB, userID int64, deviceID int, exitNode, targetType, targetValue, action, deviceIP, parentDomain string) (int64, error) {
	res, err := d.Exec(qInsertDeviceRule,
		userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FindDeviceRuleID returns the id of an existing rule matching the
// full (user, device, exit_node, target_type, target_value) tuple, or
// ErrNotFound. This is the dedup check that insertRuleUnique performs
// before inserting; the App method should compose FindDeviceRuleID +
// AppendDeviceRule.
func FindDeviceRuleID(d *sql.DB, userID int64, deviceID int, exitNode, targetType, targetValue string) (int, error) {
	var id int
	err := d.QueryRow(qSelectRuleByComposite,
		userID, deviceID, exitNode, targetType, targetValue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// FindDomainRuleID is the narrower dedup used by the form insert
// path: same as FindDeviceRuleID but target_type is implicitly
// 'domain'. Exists because the SQL string is shorter and the column
// order is the same shape — no perf difference, just readability.
func FindDomainRuleID(d *sql.DB, userID int64, deviceID int, exitNode, targetValue string) (int, error) {
	var id int
	err := d.QueryRow(qSelectDomainRuleForInsertDedup,
		userID, deviceID, exitNode, targetValue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// CountEnabledRules returns the total number of enabled device_rules
// across all users. Powers the per-user quota panel ("user X used
// N of M").
func CountEnabledRules(d *sql.DB) (int, error) {
	var n int
	err := d.QueryRow(qCountAllEnabledRules).Scan(&n)
	return n, err
}

// CountEnabledRulesForDevice returns the count of enabled rules on a
// specific device_id. The per-device 500-cap check (SKYGATE_MAX_RULES_PER_DEVICE)
// uses this.
func CountEnabledRulesForDevice(d *sql.DB, deviceID int) (int, error) {
	var n int
	err := d.QueryRow(qCountDeviceEnabledRules, deviceID).Scan(&n)
	return n, err
}

// CountEnabledNonSubnetRulesForUser returns the count of enabled,
// "logical" rules for a user — excludes the auto-derived /32 entries
// that have a non-empty parent_domain (those are children of a
// domain rule and don't count toward the per-user limit). Powers the
// per-user quota panel.
func CountEnabledNonSubnetRulesForUser(d *sql.DB, userID int64) (int, error) {
	var n int
	err := d.QueryRow(qCountEnabledUserRulesNonSubnet, userID).Scan(&n)
	return n, err
}

// CountEnabledNonSubnetRulesForUserDevice is the per-(user,device)
// guard used before each rule insert. Same /32 exclusion rule as
// CountEnabledNonSubnetRulesForUser.
func CountEnabledNonSubnetRulesForUserDevice(d *sql.DB, userID int64, deviceID int) (int, error) {
	var n int
	err := d.QueryRow(qCountRulesByUserDeviceEnabled, userID, deviceID).Scan(&n)
	return n, err
}

// CountRulesByDeviceForUser groups rules-per-device for a single
// user. The map is keyed by device_id. /32 entries with a non-empty
// parent_domain are excluded (same as the quota counters).
func CountRulesByDeviceForUser(d *sql.DB, userID int64) (CountRulesByDevice, error) {
	rows, err := d.Query(qCountRulesByDeviceGrouped, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(CountRulesByDevice)
	for rows.Next() {
		var devID, cnt int
		if err := rows.Scan(&devID, &cnt); err != nil {
			return nil, err
		}
		out[devID] = cnt
	}
	return out, rows.Err()
}

// CountRulesByExitNode groups enabled rule counts by exit_node.
// Used by the sync orchestrator and the admin /admin/exit-nodes
// page (where each exit-node shows how many routes it carries).
func CountRulesByExitNode(d *sql.DB) (CountRulesByExitNodeMap, error) {
	rows, err := d.Query(qCountRulesByExitNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(CountRulesByExitNodeMap)
	for rows.Next() {
		var name string
		var cnt int
		if err := rows.Scan(&name, &cnt); err != nil {
			return nil, err
		}
		out[name] = cnt
	}
	return out, rows.Err()
}

// CountRulesForExitNode returns the count of enabled rules routed
// through a single exit_node. Powers the per-exit-node route count
// shown on the admin /admin/exit-rules page.
func CountRulesForExitNode(d *sql.DB, exitNode string) (int, error) {
	var n int
	err := d.QueryRow(qCountRulesForExitNode, exitNode).Scan(&n)
	return n, err
}

// ListDistinctExitNodesWithRules returns the distinct exit_node_id
// values that have at least one enabled rule. Powers the GenerateACL
// builder and the staggeredSync fan-out.
func ListDistinctExitNodesWithRules(d *sql.DB) ([]string, error) {
	rows, err := d.Query(qDistinctEnabledExitNodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetDeviceRulesForUser returns every enabled rule for `userID`,
// ordered by id (stable for pagination). The DeviceName and UserName
// fields are NOT populated — they require headscale lookups and a
// JOIN, respectively; the App-level getDeviceRules fills them in
// after calling this helper.
func GetDeviceRulesForUser(d *sql.DB, userID int64) ([]DeviceRule, error) {
	rows, err := d.Query(qSelectUserRulesForView, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeviceRules(rows)
}

// GetAllRulesForAdmin returns every rule across all users, with a
// LEFT JOIN to portal_users so the user_name is filled in even if
// the user was deleted (the COALESCE in SQL returns "?" for
// orphaned rows). Used by /admin/exit-rules.
func GetAllRulesForAdmin(d *sql.DB) ([]DeviceRule, error) {
	rows, err := d.Query(qSelectAllRulesForAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeviceRule
	for rows.Next() {
		var r DeviceRule
		var en int
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNodeID, &r.TargetType,
			&r.TargetValue, &r.Action, &r.ParentDomain, &r.CreatedAt, &en,
			&r.DeviceIP, &r.UserName); err != nil {
			return nil, err
		}
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetACLEntries returns the four columns GenerateACL needs to build
// the per-device HuJSON entries. The DeviceIP is the IP the rule
// applies to; in the ACL JSON it pins the rule to a specific
// device's Tailscale address.
func GetACLEntries(d *sql.DB) ([]ACLEntry, error) {
	rows, err := d.Query(qSelectEnabledACLEntries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ACLEntry
	for rows.Next() {
		var e ACLEntry
		if err := rows.Scan(&e.TargetType, &e.TargetValue, &e.Action, &e.DeviceIP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEnabledDomainRules returns every enabled rule with
// target_type='domain'. The autoupdater walks this set to resolve
// each domain to /32 and insert derived rules.
func GetEnabledDomainRules(d *sql.DB) ([]DomainRule, error) {
	rows, err := d.Query(qSelectEnabledDomainRules)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainRule
	for rows.Next() {
		var r DomainRule
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNodeID, &r.Target, &r.Action, &r.DeviceIP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetEnabledSubnetIPRules returns the distinct (exit_node, target)
// pairs of every enabled rule with target_type in ('subnet','ip').
// Powers the per-exit-node "already enforced routes" list that
// the autoupdater uses to skip no-op inserts.
func GetEnabledSubnetIPRules(d *sql.DB) ([]SubnetIPRule, error) {
	rows, err := d.Query(qSelectEnabledSubnetIPRules)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubnetIPRule
	for rows.Next() {
		var r SubnetIPRule
		if err := rows.Scan(&r.ExitNodeID, &r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSubnetRoutesForExitNode returns the target_value of every
// enabled subnet/ip rule on a single exit_node. Used by the
// route-setup script generator.
func GetSubnetRoutesForExitNode(d *sql.DB, exitNode string) ([]string, error) {
	rows, err := d.Query(qSelectSubnetRoutesForExitNode, exitNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetUserRulesForScript returns the per-user (target_type, target,
// device_ip) rows used by the route-setup script generator. The
// user_id filter scopes to a single portal user.
func GetUserRulesForScript(d *sql.DB, userID int64) ([]ACLEntry, error) {
	rows, err := d.Query(
		"SELECT target_type, target_value, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND user_id = ?",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ACLEntry
	for rows.Next() {
		var e ACLEntry
		if err := rows.Scan(&e.TargetType, &e.TargetValue, &e.DeviceIP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetRuleTargetTypeAndParent reads (target_type, parent_domain) of a
// single rule, with a user_id ownership check. Returns ErrNotFound
// if the row doesn't exist OR doesn't belong to userID (caller
// can't distinguish, which is the point — a non-owned id looks
// identical to a missing one to prevent enumeration).
func GetRuleTargetTypeAndParent(d *sql.DB, id int, userID int64) (targetType, parentDomain string, err error) {
	err = d.QueryRow(qSelectTargetTypeByIDForDelete, id, userID).Scan(&targetType, &parentDomain)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// GetParentDomain reads just the parent_domain column of one rule.
// Used by the form insert path to decide whether to insert a fresh
// row or upsert into an existing one.
func GetParentDomain(d *sql.DB, id int) (string, error) {
	var pd string
	err := d.QueryRow(qSelectParentDomainByID, id).Scan(&pd)
	return pd, err
}

// FindSubnet32ForDomain returns every /32 rule on
// (user, device, exit_node) that carries the given parent_domain.
// Used by the autoupdater to decide which derived /32 entries
// should be re-resolved vs left alone.
func FindSubnet32ForDomain(d *sql.DB, userID int64, deviceID int, exitNode, parentDomain string) ([]struct {
	ID     int
	Target string
}, error) {
	rows, err := d.Query(qSelectSubnet32ForDomain, userID, deviceID, exitNode, parentDomain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID     int
		Target string
	}
	for rows.Next() {
		var r struct {
			ID     int
			Target string
		}
		if err := rows.Scan(&r.ID, &r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindSubnet32NoParentDomain is the pre-cascade variant of
// FindSubnet32ForDomain: same shape, but no parent_domain filter.
// Used during a fresh domain-rule insert to discover any orphaned
// /32 entries that should be replaced by the new rule.
func FindSubnet32NoParentDomain(d *sql.DB, userID int64, deviceID int, exitNode string) ([]struct {
	ID     int
	Target string
}, error) {
	rows, err := d.Query(qSelectSubnet32NoParentDomain, userID, deviceID, exitNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID     int
		Target string
	}
	for rows.Next() {
		var r struct {
			ID     int
			Target string
		}
		if err := rows.Scan(&r.ID, &r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRule removes a single rule by id, with no ownership check.
// Use this only from contexts that already verified ownership
// (e.g. admin actions, autoupdater). User-initiated deletes go
// through DeleteRuleForUser.
func DeleteRule(d *sql.DB, id int) error {
	_, err := d.Exec(qDeleteRuleByID, id)
	return err
}

// DeleteRuleForUser removes a single rule by id, with a user_id
// ownership check. If the rule doesn't exist OR doesn't belong to
// userID, zero rows are deleted (the call still returns nil —
// the DELETE is idempotent).
func DeleteRuleForUser(d *sql.DB, id int, userID int64) error {
	_, err := d.Exec(qDeleteRulesByIDAndUser, id, userID)
	return err
}

// DeleteRuleOrCascadeByParentDomain removes a rule by id, OR — if
// the rule is a subnet /32 with a parent_domain — also removes the
// entire family of /32 rules with the same parent_domain. The
// user_id check keeps the cascade from leaking across users.
// parentDomain is the COALESCE(parent_domain,'') of the row being
// deleted (pass the empty string if it's already empty).
//
// Returns the number of rows deleted (the original id + any /32
// children with matching parent_domain). The caller can subtract 1
// to count the "extra cascade" rows.
func DeleteRuleOrCascadeByParentDomain(d *sql.DB, userID int64, id int, parentDomain string) (int64, error) {
	res, err := d.Exec(qDeleteRulesByIDOrParentDomain, userID, id, parentDomain)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkUpdateDeviceIP sets device_ip on every row whose device_id is
// in deviceIDs and whose device_ip is currently empty or NULL.
// Used by the admin /admin/exit-rules/cleanup endpoint to backfill
// the device_ip column after a migration.
func BulkUpdateDeviceIP(d *sql.DB, deviceIP string, deviceIDs []int) error {
	if len(deviceIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(deviceIDs))
	args := make([]any, 0, len(deviceIDs)+1)
	args = append(args, deviceIP)
	for i, id := range deviceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := "UPDATE device_rules SET device_ip = ? WHERE (device_ip = '' OR device_ip IS NULL) AND device_id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := d.Exec(q, args...)
	return err
}

// BulkReassignDeviceID rewrites every rule whose device_id is in
// oldIDs to use newID instead. Used by the admin cleanup endpoint
// to merge duplicate device_ids (e.g. after a headscale re-register
// created a new id for the same physical machine).
func BulkReassignDeviceID(d *sql.DB, newID int, oldIDs []int) error {
	if len(oldIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(oldIDs))
	args := make([]any, 0, len(oldIDs)+1)
	args = append(args, newID)
	for i, id := range oldIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := "UPDATE device_rules SET device_id = ? WHERE device_id IN (" + strings.Join(placeholders, ",") + ")"
	_, err := d.Exec(q, args...)
	return err
}

// scanDeviceRules reads a *sql.Rows from one of the user-scoped
// rule queries and returns []DeviceRule. Column order must match
// qSelectUserRulesForView.
func scanDeviceRules(rows *sql.Rows) ([]DeviceRule, error) {
	var out []DeviceRule
	for rows.Next() {
		var r DeviceRule
		var en int
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNodeID,
			&r.TargetType, &r.TargetValue, &r.Action, &r.DeviceIP, &en, &r.ParentDomain); err != nil {
			return nil, err
		}
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
