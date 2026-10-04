// device_index_b348.go — B348 (2026-10-04).
//
// A DEVICE MUST NEVER BE INVISIBLE BECAUSE OF PAGINATION.
//
// Operator report, immediately after adding a rule for cyborg:
// «правила пропали у cyborg у пользователя добавились но админ не видит также
// пропали правила из админ страницы exit rules и по остальным устройствам что не
// skyworker».
//
// Measured on the live host: nothing was lost — `device_rules` held 12 rows for
// cyborg, 215 for skyworker and 89 for basic, all enabled. What the pages showed
// was the pagination WINDOW, not the data:
//
//	/my/exit-rules      page 1 of 5, "Текущие правила (50)" → ONLY skyworker's
//	                    rows, so cyborg had no group at all
//	/admin/exit-rules   page 1 of 7 → ONLY skyworker (50 правил)
//
// Page 1 was all skyworker because the auto-updater's CDN re-resolve pass had just
// added ~54 new /32 rows for that device: a row-level window gives the page to
// whichever device owns the most rows. B347 fixed the assignment MARKER (the strip
// names cyborg and its relay on page 1), but the RULE LIST was still page-bound,
// and a device with no rows in the window looks exactly like a device with no
// rules at all.
//
// The fix is navigation by DEVICE, not by row: `DeviceRuleCounts*` returns the
// complete inventory — every device with enabled rules and how many it has — in ONE
// unpaginated query, and every entry links to a per-device drill-down. A device is
// therefore always one click away, whatever page the row list is on.
package exit_rules

import (
	"database/sql"

	"skygate/internal/db"
)

// DeviceRuleCount is one inventory line: a device (optionally attributed to a
// user) and how many ENABLED rules it has.
type DeviceRuleCount struct {
	UserName string
	Hostname string
	Rules    int
}

// DeviceRuleCountsForUser is the /my/exit-rules inventory: every device of ONE
// user with enabled rules, unpaginated. It is the same "enabled rules, real
// hostname" definition B343/B347 use, so the three cannot disagree about which
// devices exist.
func DeviceRuleCountsForUser(d *sql.DB, userID int64) ([]DeviceRuleCount, error) {
	return deviceRuleCounts(d, `
		SELECT '' AS user_name, r.device_hostname, COUNT(*)
		  FROM device_rules r
		 WHERE r.user_id = $1
		   AND r.enabled = 1
		   AND r.device_hostname <> ''
		 GROUP BY r.device_hostname
		 ORDER BY r.device_hostname`, userID)
}

// DeviceRuleCountsForAdmin is the cross-user inventory behind the
// /admin/exit-rules index. The denormalised `user_name` is used when present (it
// is, for rows written since v0.28) and an empty value means the row predates the
// backfill — the template then shows the same "—" the table does rather than
// inventing an owner.
func DeviceRuleCountsForAdmin(d *sql.DB) ([]DeviceRuleCount, error) {
	return deviceRuleCounts(d, `
		SELECT COALESCE(r.user_name, ''), r.device_hostname, COUNT(*)
		  FROM device_rules r
		 WHERE r.enabled = 1
		   AND r.device_hostname <> ''
		 GROUP BY COALESCE(r.user_name, ''), r.device_hostname
		 ORDER BY COALESCE(r.user_name, ''), r.device_hostname`)
}

// deviceRuleCounts runs either inventory; both share one scan/error path, and
// `queryArgs` carries the optional user scope.
func deviceRuleCounts(d *sql.DB, query string, queryArgs ...interface{}) ([]DeviceRuleCount, error) {
	out := []DeviceRuleCount{}
	rows, err := d.Query(query, queryArgs...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var row DeviceRuleCount
		if err := rows.Scan(&row.UserName, &row.Hostname, &row.Rules); err != nil {
			continue
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeviceRuleCountsForUserService / …ForAdminService are the Service-scoped forms
// (the B343/B347 pattern), so the handlers do not need the raw handle.
func (s *Service) DeviceRuleCountsForUserService(userID int64) ([]DeviceRuleCount, error) {
	return DeviceRuleCountsForUser(s.dbc(), userID)
}

func (s *Service) DeviceRuleCountsForAdminService() ([]DeviceRuleCount, error) {
	return DeviceRuleCountsForAdmin(s.dbc())
}

// filterRulesByDevice is the server-side half of the drill-down: given ALL of a
// user's rules it returns only the named device's.
//
// Two keys are accepted on purpose. The page groups rules by `DeviceName` (a
// headscale lookup that `enrichDeviceNames` fills, falling back to the raw device
// id), while the drill-down LINK carries the denormalised `device_hostname` the
// inventory query returns — the same value the B347 strip prints. Matching either
// the displayed name or the resolved device id means the link works whether or not
// enrichment found a name for that node (a device that is currently offline still
// has rules, and those rules must stay reachable).
//
// `deviceIDs` is the set the caller resolved from the same DeviceInfos the page
// renders; nil is fine (name-only matching).
func filterRulesByDevice(rules []db.DeviceRule, device string, deviceIDs map[int]bool) []db.DeviceRule {
	if device == "" {
		return rules
	}
	out := make([]db.DeviceRule, 0, len(rules))
	for _, r := range rules {
		if equalFoldASCII(r.DeviceName, device) || (deviceIDs != nil && deviceIDs[r.DeviceID]) {
			out = append(out, r)
		}
	}
	return out
}

// equalFoldASCII compares two device names case-insensitively. Device names are
// hostnames (ASCII); a locale-aware comparison would make the result depend on
// the process locale, which is exactly the kind of difference that makes a filter
// behave one way in tests and another way in production.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
