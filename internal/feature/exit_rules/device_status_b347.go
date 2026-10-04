// device_status_b347.go — B347 (2026-10-04).
//
// THE DEVICE → RELAY ASSIGNMENT MUST NOT BE PAGINATED AWAY.
//
// B343 made the state visible ON the page: a device with enabled rules and no
// exit-node preference gets a red badge, a pinned device gets a green
// «выход через: <relay>». But those markers live inside the per-device rule
// GROUPS, and the groups are built from the CURRENT PAGE of rule rows. Measured
// live on 2026-10-04 (the operator's own account, the same cyborg report):
//
//	/my/exit-rules rendered "Страница 1 из 5 (всего 228 правил)" with the
//	default page size of 50, and page 1 contained ONLY skyworker's rows.
//	The device picker offered «cyborg (1/500)» and the page showed no cyborg
//	group and no badge for it at all — so adding a rule for cyborg produced no
//	visible change anywhere, which is exactly what the operator reported:
//	«нигде не маркируется что у cyborg доступ теперь появился».
//
// The fix is the strip below the form and ABOVE the groups: one row per device
// that has enabled rules, computed from the WHOLE rule set (no LIMIT, no page
// window) in a single query, naming the relay the device is pinned to — or
// saying plainly that it is not pinned. The paginated groups keep working as
// they did; the strip is what makes the answer page-independent.
package exit_rules

import (
	"database/sql"
	"strings"
)

// DeviceStatusRow is one line of the strip: what the operator needs to answer
// "does THIS device have access, and through which relay?" without paging.
type DeviceStatusRow struct {
	Hostname string
	Rules    int
	// Relay is the per-(user, device) preference, WITHOUT the `tag:` prefix —
	// the same value the B343 badge prints next to «выход через:». Empty means
	// the device has enabled rules and NO preference: inert until the
	// reconciler derives one (B341.1) or the operator picks it.
	Relay string
	// Pinned is Relay != "" spelled out, so a template (or a JSON consumer) does
	// not have to re-derive it from an empty string.
	Pinned bool
}

// tagPrefix is the per-node tag prefix the preference table stores. The strip
// shows the HOSTNAME (`emilia`), because that is what the operator types into
// `tailscale --exit-node=` and what every other page prints.
const tagPrefix = "tag:"

// DeviceStatusRows returns, for one portal user, EVERY device with enabled
// rules and the relay it is pinned to (empty when it is not pinned).
//
// Deliberately ONE query with no LIMIT: the whole point of B347 is that the
// answer must not depend on the current page of the rule table. The LEFT JOIN
// is the same definition B343 uses (`p.device_hostname IS NULL` == "no
// preference row"), and empty hostnames are skipped for the same reason — they
// are the pre-rename leftovers the reconciler matches through `node_owner_map`,
// and naming them would send the operator looking for a device that does not
// exist.
//
// Placeholders are the universal `$1` form and the join is on TEXT columns
// only, so SQLite and PostgreSQL answer identically (the B319 lesson: a
// device_id ↔ node_id comparison is INTEGER vs TEXT on PostgreSQL).
func DeviceStatusRows(d *sql.DB, userID int64) ([]DeviceStatusRow, error) {
	out := []DeviceStatusRow{}
	rows, err := d.Query(`
		SELECT r.device_hostname,
		       COUNT(*),
		       COALESCE(MAX(p.exit_node_tag), '')
		  FROM device_rules r
		  LEFT JOIN device_exit_node_prefs p
		         ON p.user_id = r.user_id
		        AND p.device_hostname = r.device_hostname
		 WHERE r.user_id = $1
		   AND r.enabled = 1
		   AND r.device_hostname <> ''
		 GROUP BY r.device_hostname
		 ORDER BY r.device_hostname`, userID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var row DeviceStatusRow
		if err := rows.Scan(&row.Hostname, &row.Rules, &row.Relay); err != nil {
			continue
		}
		row.Relay = strings.TrimPrefix(row.Relay, tagPrefix)
		row.Pinned = row.Relay != ""
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeviceStatusRowsForService is the same read through the service's DBSource,
// so the page handler does not need the raw handle (the B343 pattern).
func (s *Service) DeviceStatusRowsForService(userID int64) ([]DeviceStatusRow, error) {
	return DeviceStatusRows(s.dbc(), userID)
}

// AssignedRelayFor answers "which relay does this device go through right now?"
// for ONE device — the question the post-save flash has to answer. Returns ""
// when the device is not pinned; the caller then says nothing extra, because
// the strip at the top of the same page already carries the red marker.
func AssignedRelayFor(d *sql.DB, userID int64, hostname string) string {
	if strings.TrimSpace(hostname) == "" {
		return ""
	}
	var tag string
	err := d.QueryRow(`
		SELECT exit_node_tag
		  FROM device_exit_node_prefs
		 WHERE user_id = $1
		   AND device_hostname = $2`, userID, hostname).Scan(&tag)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(tag, tagPrefix)
}
