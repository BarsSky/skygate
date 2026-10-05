// rule_owner_b351.go — B351 (2026-10-05).
//
// The domain auto-updater inserts rows on every tick (the CDN range expansion and
// the per-IP /32 fallback) and its two raw INSERTs list neither `user_name` nor
// `device_hostname`. The one-time V0.44 migration filled user_name for the rows
// that existed then; nothing ever filled it afterwards, so on the live deployment
// 220 of 349 rows — including ALL 68 rows of `basic` — carried an empty owner.
//
// That is the "wrong key" the operator suspected: `DeviceRuleCountsForAdmin`
// (B348) groups by that column, so the /admin/exit-rules device index listed one
// device twice, once under its owner and once under "". The device's ACCESS was
// never affected (all 68 of `basic`'s rules named `karolina`, the prefix owner,
// which advertises and has all 188 routes approved) — only the bookkeeping lied.
//
// This lookup resolves the pair the INSERTs must write, with one query per unique
// (user, device) per pass. An empty answer is cached too: a device missing from
// node_owner_map must not re-query for every CIDR of every domain.
package exit_rules

import (
	"database/sql"
)

// qRuleOwnerForDevice resolves the owner pair from the two authoritative tables,
// never from device_rules itself (the column being repaired is the one being
// read, so a self-referential lookup would copy the blank forward).
//
// The CAST mirrors the B349 drill-down query: node_owner_map.node_id is stored as
// text while device_rules.device_id is an integer, and `CAST(x AS INTEGER)` is
// portable to both dialects.
const qRuleOwnerForDevice = `SELECT ` +
	`COALESCE((SELECT username FROM portal_users WHERE id = $1), ''), ` +
	`COALESCE((SELECT hostname FROM node_owner_map WHERE CAST(node_id AS INTEGER) = $2 LIMIT 1), '')`

// ruleOwnerLookup caches the (user_name, device_hostname) pair per (user, device)
// for the duration of one auto-updater pass.
type ruleOwnerLookup struct {
	db    *sql.DB
	cache map[[2]int]ruleOwnerPair
}

type ruleOwnerPair struct {
	userName string
	hostname string
}

func newRuleOwnerLookup(d *sql.DB) *ruleOwnerLookup {
	return &ruleOwnerLookup{db: d, cache: map[[2]int]ruleOwnerPair{}}
}

// get returns the pair to store on a new rule row. A lookup error degrades to the
// empty pair rather than failing the tick: the ACL builder resolves the owner from
// node_owner_map by device_id since B265, so a row with an empty pair still works,
// and the maintenance backfill repairs it on the next pass.
func (l *ruleOwnerLookup) get(userID int64, deviceID int) (string, string) {
	key := [2]int{int(userID), deviceID}
	if p, ok := l.cache[key]; ok {
		return p.userName, p.hostname
	}
	var pair ruleOwnerPair
	_ = l.db.QueryRow(qRuleOwnerForDevice, userID, deviceID).Scan(&pair.userName, &pair.hostname)
	l.cache[key] = pair
	return pair.userName, pair.hostname
}
