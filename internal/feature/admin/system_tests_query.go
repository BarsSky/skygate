// system_tests_query.go — the dialect-specific query + import guards.
//
// Split out of system_tests.go in refactor Phase D (2026-10-01): B282's
// preferredMismatchRulesQuery (the SQLite/PG text-cast difference) and the `var _`
// guards that keep the imports honest.

package admin

import (
	"fmt"

	"skygate/internal/db"
)

// preferredMismatchRulesQuery builds the `exit_rules.preferred_mismatch`
// rule query for the given dialect.
//
// B282 (2026-09-22). The join needs a text cast because
// node_owner_map.node_id is TEXT (headscale's machine key as a string)
// while device_rules.device_id is the INTEGER autoincrement:
//
//   - PostgreSQL needs it — without the cast the comparison is a hard
//     error ("operator does not exist: text = integer", SQLSTATE 42883);
//   - SQLite must NOT see the PG `::` shorthand — it has no such token
//     and the whole statement fails to parse, which is what the live
//     native `aro` host reported as
//     `query rules: SQL logic error: unrecognized token: ":"`.
//
// Pure so both forms can be pinned without a database.
func preferredMismatchRulesQuery(kind db.DialectKind) string {
	return fmt.Sprintf(`
				SELECT r.user_id, COALESCE(d.hostname, ''), r.exit_node_id
				  FROM device_rules r
				  LEFT JOIN node_owner_map d ON d.node_id = %s
				 WHERE r.enabled = 1 AND r.exit_node_id != ''`,
		kind.CastText("r.device_id"))
}
