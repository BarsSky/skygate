// inert_rules_b343.go — B343 (2026-10-02, operator report).
//
// "ПРАВИЛО ЕСТЬ, А ДОСТУПА НЕТ" MUST BE VISIBLE ON THE PAGE, NOT ONLY IN THE
// JOURNAL.
//
// The operator's words: «в интерфейсе всплывает уведомление что правило уже есть
// но нигде не маркируется что у cyborg доступ теперь появился и соответственно
// доступа тоже на него не распространяется». The notice they saw
// (`exit_rules.duplicate` — "домен уже покрыт правилом") answers "did I just add
// a second copy?", which is a different question from "does this device actually
// route through a relay NOW?".
//
// The state that was invisible: a device with ENABLED rules and NO
// `device_exit_node_prefs` row. Its rules are inert — nothing pins the device to
// an exit node, so the client never selects one — while the generated ACL looks
// correct, because since B275 the per-CIDR `via=` pins come from `prefix_owner`
// and not from the rule. Measured live on 2026-10-02: `cyborg` had 11 youtube
// rules, no preference, and every panel page said nothing at all.
//
// B341/B341.1 made the reconciler derive the relay automatically, so most of
// these devices heal within one tick. This file is the half that stays true when
// the derivation CANNOT decide (no owner / split owner / owner without a per-node
// tag): the pages now name the device and its rule count instead of staying
// silent.
package exit_rules

import (
	"database/sql"
	"fmt"
)

// DevicesWithoutExitNodePref returns, for one portal user, the device hostnames
// that have ENABLED rules and no per-device exit-node preference, with the number
// of enabled rules behind each. An empty map (not an error) means every device
// with rules is pinned.
//
// The LEFT JOIN is the whole definition: `p.device_hostname IS NULL` is exactly
// "there is no preference row for this (user, device)". Rows whose
// `device_hostname` is empty are skipped — they are the pre-rename leftovers the
// reconciler matches through `node_owner_map`, and naming them on the page would
// send the operator looking for a device that does not exist.
func DevicesWithoutExitNodePref(d *sql.DB, userID int64) (map[string]int, error) {
	out := map[string]int{}
	rows, err := d.Query(`
		SELECT r.device_hostname, COUNT(*)
		  FROM device_rules r
		  LEFT JOIN device_exit_node_prefs p
		         ON p.user_id = r.user_id
		        AND p.device_hostname = r.device_hostname
		 WHERE r.user_id = $1
		   AND r.enabled = 1
		   AND r.device_hostname <> ''
		   AND p.device_hostname IS NULL
		 GROUP BY r.device_hostname
		 ORDER BY r.device_hostname`, userID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var host string
		var n int
		if err := rows.Scan(&host, &n); err != nil {
			continue
		}
		out[host] = n
	}
	return out, rows.Err()
}

// DevicesWithoutExitNodePrefForService is the same query through the service's
// DBSource, so callers that hold a Service (both rule pages) do not need the raw
// handle.
func (s *Service) DevicesWithoutExitNodePrefForService(userID int64) (map[string]int, error) {
	return DevicesWithoutExitNodePref(s.dbc(), userID)
}

// inertDeviceLabels is a stable, human-readable list ("cyborg (11), tablet (3)")
// for the banner, so the page does not depend on Go's map iteration order.
func inertDeviceLabels(m map[string]int) []string {
	names := make([]string, 0, len(m))
	for h := range m {
		names = append(names, h)
	}
	// Small sets: insertion sort keeps this dependency-free and deterministic.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	out := make([]string, 0, len(names))
	for _, h := range names {
		out = append(out, fmt.Sprintf("%s (%d)", h, m[h]))
	}
	return out
}
