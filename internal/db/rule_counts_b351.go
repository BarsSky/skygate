// rule_counts_b351.go — B351 (2026-10-05).
//
// TWO DEFECTS, ONE ROOT: the admin page measured the PAGE, and the auto-updater
// wrote rows without their owner.
//
// Operator report (screenshot of /admin/exit-rules):
//
//	«Проверь доступность правил для пользователя michail от basic почему то правил
//	 7 страниц у админа, и при этом на каждой по разному, посмотри что не так с
//	 отображением и нет ли обновлятора сервиса что переписывает в неправильном ключе»
//
// Measured on the live PostgreSQL deployment (349 enabled rows, 2 users with rules):
//
//  1. THE COUNTERS WERE PAGE-SCOPED. `qSelectAllRulesForAdminPaged` is
//     `ORDER BY r.id LIMIT 50 OFFSET n`, and the CDN auto-updater inserts new
//     rows for every device on every tick, so one (user, device) group is
//     scattered over many pages. The page then built its user/device groups AND
//     their counters from THAT slice: the same user read «19 правил / 3%
//     (19/500)» on page 6 and «49 правил / 9% (49/500)» on page 7, while the
//     heading said «ВСЕ ПРАВИЛА (50)» next to a pagination line saying «всего 349
//     правил». Every page told a different story and none of them was the
//     database's.
//
//  2. THE DENORMALISED OWNER WAS EMPTY FOR MOST ROWS. `device_rules.user_name` is
//     filled by exactly one thing: the one-time V0.44 migration. The runtime
//     backfill that exists for `device_hostname`
//     (`UpdateDeviceRuleHostnameForNode`) has NO twin for the username, and the
//     auto-updater's two raw INSERTs (sync.go: the CDN expansion and the /32
//     fallback) list neither column. Live: **220 of 349 rows had an empty
//     user_name**, including ALL 68 rows of `basic` — and `DeviceRuleCountsForAdmin`
//     (B348) GROUPs BY that column, so the admin index listed one device twice:
//     once under its owner and once under "". `basic`'s ACCESS was fine (all 68
//     rules named `karolina`, the prefix owner, which advertises and has all 188
//     of its routes approved) — what was broken was the bookkeeping, exactly as
//     the operator suspected («обновлятор переписывает в неправильном ключе»).
//
// This file gives the page REAL, unpaginated counts at the levels it displays and
// provides the healer for the owner column.
package db

import (
	"database/sql"
	"strconv"
)

// AdminRuleCounts holds the real counters behind the /admin/exit-rules badges.
//
// The composite maps are keyed by strings because neither dialect has a portable
// composite key; use UserDeviceKey / UserDeviceExitKey so the format lives in one
// place and cannot drift between the producer and the reader.
type AdminRuleCounts struct {
	// EnabledByUser is every enabled rule of a user, across all their devices.
	EnabledByUser map[int64]int
	// UserFacingByUser is the QUOTA unit (B328): enabled AND
	// (target_type != 'subnet' OR parent_domain = ''). It must be the same number
	// the insert guard compares against MaxPerUser, or the badge quotes a limit
	// that is not the one being enforced.
	UserFacingByUser map[int64]int
	// EnabledByUserDevice is the real per-device count, keyed "userID:deviceID".
	EnabledByUserDevice map[string]int
	// EnabledByUserDeviceExit is the real per-(device, relay) count, keyed
	// "userID:deviceID:exitNodeID".
	EnabledByUserDeviceExit map[string]int
}

// UserDeviceKey is the EnabledByUserDevice key format.
func UserDeviceKey(userID int64, deviceID int) string {
	return strconv.FormatInt(userID, 10) + ":" + strconv.Itoa(deviceID)
}

// UserDeviceExitKey is the EnabledByUserDeviceExit key format.
func UserDeviceExitKey(userID int64, deviceID int, exitNode string) string {
	return UserDeviceKey(userID, deviceID) + ":" + exitNode
}

// AdminRuleCounters reads every counter the admin page needs. Four GROUP BY
// queries (a GROUPING SETS query is not portable), each one a scan over
// `enabled = 1`, so the cost does not grow with the page size — which is the
// whole point: the page must stop deriving totals from its window.
func AdminRuleCounters(d *sql.DB) (AdminRuleCounts, error) {
	out := AdminRuleCounts{
		EnabledByUser:           map[int64]int{},
		UserFacingByUser:        map[int64]int{},
		EnabledByUserDevice:     map[string]int{},
		EnabledByUserDeviceExit: map[string]int{},
	}

	rows, err := d.Query(qCountEnabledRulesByUser)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var uid int64
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			continue
		}
		out.EnabledByUser[uid] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = d.Query(qCountUserFacingRulesByUser)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var uid int64
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			continue
		}
		out.UserFacingByUser[uid] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = d.Query(qCountEnabledRulesByUserDevice)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var uid int64
		var did, n int
		if err := rows.Scan(&uid, &did, &n); err != nil {
			continue
		}
		out.EnabledByUserDevice[UserDeviceKey(uid, did)] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	rows, err = d.Query(qCountEnabledRulesByUserDeviceExit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var did, n int
		var exit string
		if err := rows.Scan(&uid, &did, &exit, &n); err != nil {
			continue
		}
		out.EnabledByUserDeviceExit[UserDeviceExitKey(uid, did, exit)] = n
	}
	return out, rows.Err()
}

// BackfillDeviceRuleUserNames fills the denormalised `user_name` on every row that
// still has an empty one, resolving it from portal_users. It is the missing twin of
// UpdateDeviceRuleHostnameForNode and is safe on every maintenance tick: the WHERE
// clause matches nothing once the table is healed.
//
// Rows whose portal_users row is gone are left alone — the ACL builder resolves the
// owner from node_owner_map by device_id since B265, so such a row still works, and
// inventing a username for it would be worse than an empty one.
func BackfillDeviceRuleUserNames(d *sql.DB) (int64, error) {
	res, err := d.Exec(qBackfillDeviceRuleUserNames)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
