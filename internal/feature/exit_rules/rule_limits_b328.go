// rule_limits_b328.go — B328: the rule caps must count what they say they count.
//
// OPERATOR REPORT (2026-09-25, /my/exit-rules):
//
//	«не получилось добавить правило на другое устройство пользователя»
//	device limit exceeded: 500/500 user-facing rules on this device
//	(auto-resolved /32 IP rules не учитываются)   ← while the picker above it
//	                                                  showed cyborg (1/500)
//
// MEASURED ROOT CAUSE. `PostMyExitRule` compared the per-device cap against
//
//	countUserFacing(0, devID, false)
//
// and the closure behind it had three modes — (user+device), (user), (everything) —
// with NO "device without user" case, so `userID == 0` fell through to the default
// branch and the guard read `db.CountEnabledRules`:
//
//	SELECT COUNT(*) FROM device_rules WHERE enabled = 1        -- SYSTEM-WIDE
//
// Live on the operator's host that number was exactly 500 (9→263, 29→236, 56→1
// enabled rows) against SKYGATE_MAX_RULES_PER_DEVICE=500, so **every rule insert for
// every user was refused** while the page correctly showed the same device as 1/500
// (the per-(user,device) non-subnet count). The message named the device and printed
// a system total.
//
// WHY IT IS A REGRESSION, NOT A DESIGN CHOICE. The correct helper already existed and
// carried a doc comment saying it was the one this check uses
// (db.CountEnabledRulesForDevice: "The per-device ... check
// (SKYGATE_MAX_RULES_PER_DEVICE) uses this"), and the admin path
// (form_admin.go PostAdminExitRule) calls the right one:
// db.CountEnabledNonSubnetRulesForUserDevice. Only the /my path degraded, when the
// checks were folded into that closure ("Этап 9 part 2").
//
// THE SHAPE OF THE FIX. The ladder is now a pure decision over an explicit counts
// struct, and every count is produced by a helper that REQUIRES the identity it
// counts for. There is no parameter combination that means "global" by accident, so
// the ambiguity cannot come back: `PerDevice` can only be filled by a (user, device)
// query, `PerUser` only by a user query, `Total` only by the system query.
//
// The unit of measure is the one the operator sees and the one the admin path already
// used — enabled AND (target_type != 'subnet' OR parent_domain is empty). MEASURED
// composition on the live host (500 enabled rows), because that predicate is less
// obvious than its old comment claimed:
//
//	subnet + parent_domain set  454 rows  EXCLUDED  the CIDR ranges a domain rule
//	                                                 expanded into (discord.com,
//	                                                 ghcr.io, quay.io, …)
//	domain + parent_domain set   20 rows  COUNTS    the per-domain row the CDN
//	                                                 short-circuit and B184's status
//	                                                 read — B298 keeps it on purpose
//	subnet, no parent_domain     17 rows  COUNTS    hand-entered subnets
//	domain, no parent_domain      9 rows  COUNTS    hand-entered domains
//
// So the quota consumes what the operator asked for plus the per-domain rows the
// expander keeps, and ignores the range bulk it produced on their behalf. There are no
// ip-typed derived rows in the live data at all, which is why the operator-facing text
// no longer talks about "/32 IP rules". B328 also brings the API path to this unit
// (api.go).
package exit_rules

import (
	"database/sql"
	"fmt"

	"skygate/internal/db"
)

// ruleLimits is the configured ladder. 0 disables that level.
type ruleLimits struct {
	MaxPerUser   int
	MaxPerDevice int
	MaxTotal     int
}

// ruleLimitCounts is the MEASURED state the ladder is compared against. Each field
// has exactly one legitimate producer (the count* helpers below), which is the
// structural half of the B328 fix.
type ruleLimitCounts struct {
	// Username only appears in the operator-facing message.
	Username string
	// PerUser is this user's user-facing rules across all their devices.
	PerUser int
	// PerDevice is this user's user-facing rules ON THIS DEVICE.
	PerDevice int
	// Total is every enabled rule system-wide.
	Total int
}

// countUserFacingForUser counts a user's user-facing (non-subnet) rules.
func countUserFacingForUser(d *sql.DB, userID int64) int {
	n, _ := db.CountEnabledNonSubnetRulesForUser(d, userID)
	return n
}

// countUserFacingForUserDevice counts ONE device's user-facing rules for ONE user.
// Both identities are required parameters: there is no call that omits the user and
// silently gets the system total.
func countUserFacingForUserDevice(d *sql.DB, userID int64, deviceID int) int {
	n, _ := db.CountEnabledNonSubnetRulesForUserDevice(d, userID, deviceID)
	return n
}

// countAllEnabledRules is the SYSTEM-WIDE count. It exists for the MaxTotalRules
// ceiling only; nothing in the per-user or per-device path may use it.
func countAllEnabledRules(d *sql.DB) int {
	n, _ := db.CountEnabledRules(d)
	return n
}

// ExceedReason returns "" when one more rule fits, or the reason it does not.
//
// Pure (no DB, no config) on purpose: the exact live shape that produced the operator
// report is a test case — 500 system-wide, 1 user-facing on the device, cap 500 must
// be ALLOWED, and the pre-B328 code refused it.
func (l ruleLimits) ExceedReason(c ruleLimitCounts) string {
	if l.MaxPerUser > 0 && c.PerUser >= l.MaxPerUser {
		return fmt.Sprintf("user limit exceeded: %d/%d rules for user %s (auto-resolved /32 IP rules не учитываются)",
			c.PerUser, l.MaxPerUser, c.Username)
	}
	if l.MaxPerDevice > 0 && c.PerDevice >= l.MaxPerDevice {
		return fmt.Sprintf("device limit exceeded: %d/%d user-facing rules on this device (auto-resolved /32 IP rules не учитываются)",
			c.PerDevice, l.MaxPerDevice)
	}
	if l.MaxTotal > 0 && c.Total >= l.MaxTotal {
		return fmt.Sprintf("system limit exceeded: %d/%d user-facing rules", c.Total, l.MaxTotal)
	}
	return ""
}

// measureRuleLimits fills the counts the ladder needs, asking the DB ONLY for the
// levels that are actually enabled — so a run with no configured cap stays free of
// queries, exactly as before B328.
func measureRuleLimits(d *sql.DB, l ruleLimits, userID int64, deviceID int, username string) ruleLimitCounts {
	c := ruleLimitCounts{Username: username}
	if l.MaxPerUser > 0 {
		c.PerUser = countUserFacingForUser(d, userID)
	}
	if l.MaxPerDevice > 0 {
		c.PerDevice = countUserFacingForUserDevice(d, userID, deviceID)
	}
	if l.MaxTotal > 0 {
		c.Total = countAllEnabledRules(d)
	}
	return c
}
