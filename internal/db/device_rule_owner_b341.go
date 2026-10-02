// internal/db/device_rule_owner_b341.go — B341 (2026-10-01).
//
// WHY THIS EXISTS. A device rule may carry an EMPTY exit_node_id: the operator
// adds "youtube.com" and never picks a relay, and the CDN expansion copies that
// emptiness into every resolved subnet row. The ACL then still pins those prefixes
// — since B275 the per-CIDR pin comes from prefix_owner(), not from the rule — so
// the policy looks correct while the DEVICE has no exit node at all, and the rule
// has no effect on the client. Live case: device `cyborg` had 11 such rules
// (youtube) and no device_exit_node_prefs row, while `skyworker` — whose rules do
// name a relay — worked.
//
// The decision the reconciler needs in that case is "which relay would the DATA
// PLANE use for this device's rules?", and that is the assignment table. This
// helper answers exactly that question, per OWNER, from the same table the ACL
// pin comes from — so the two halves of the decision cannot name different
// relays.
//
// PORTABILITY: `$N` placeholders and `CAST(device_id AS TEXT)` are the two forms
// that work on BOTH backends (see B319/B331: device_rules.device_id is INTEGER
// while node_owner_map.node_id is TEXT, so PostgreSQL refuses a bare comparison;
// `$N` binds by ordinal on modernc.org/sqlite). Do not rewrite this query for one
// backend — tests run it against SQLite always and PostgreSQL when
// SKYGATE_TEST_PG_DSN is set.

package db

import (
	"database/sql"
	"fmt"
)

// OwnerTagCountsForDeviceRules returns, for one (user, device), how many of the
// device's ENABLED subnet/ip rules cover a prefix that a given relay owns in
// prefix_owner: `owner hostname -> rule count`.
//
// Only subnet/ip rules are counted: a DOMAIN rule's target_value ("youtube.com")
// never appears in prefix_owner.prefix, and the ACL pins exist per resolved CIDR —
// so counting the domain row would compare different populations (the same
// mistake contract X of B188.2 made and had to renegotiate on 2026-10-01).
//
// An empty map with a nil error means "this device's rules cover no prefix that
// has an owner", which the caller must report rather than treat as "no data".
func OwnerTagCountsForDeviceRules(d *sql.DB, userID int64, hostname string) (map[string]int, error) {
	if d == nil {
		return nil, fmt.Errorf("OwnerTagCountsForDeviceRules: nil db")
	}
	if hostname == "" {
		return map[string]int{}, nil
	}
	rows, err := d.Query(`
		SELECT po.exit_node_id, COUNT(*)
		  FROM device_rules r
		  JOIN prefix_owner po ON po.prefix = r.target_value
		 WHERE r.user_id = $1
		   AND r.enabled = 1
		   AND r.target_type IN ('subnet', 'ip')
		   AND (
		     r.device_hostname = $2
		     OR CAST(r.device_id AS TEXT) IN (
		       SELECT node_id FROM node_owner_map WHERE hostname = $2
		     )
		   )
		 GROUP BY po.exit_node_id
		 ORDER BY COUNT(*) DESC, po.exit_node_id ASC
	`, userID, hostname)
	if err != nil {
		return nil, fmt.Errorf("owner tags for %s: %w", hostname, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var owner string
		var n int
		if err := rows.Scan(&owner, &n); err != nil {
			return nil, err
		}
		if owner == "" {
			continue
		}
		out[owner] += n
	}
	return out, rows.Err()
}
