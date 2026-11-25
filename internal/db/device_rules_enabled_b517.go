// device_rules_enabled_b517.go — B517: writing device_rules.enabled.
//
// THE COLUMN HAS EXISTED SINCE v0.20 AND EVERY CONSUMER ALREADY HONOURS IT: the
// ACL entry query is `WHERE enabled = 1`, the advertised-route sync, the DNS
// auto-updater, the preferred-exit reconciler, the prefix claims and every quota
// counter filter on it too. What did not exist was a WRITER — the panel could
// only add or delete, so "switch this rule off for a while" meant destroying the
// rule (and with it its parent_domain, its group label and its place in the
// prefix assignment).
//
// The write is OWNER-SCOPED, and that is the security boundary of the whole
// feature: `WHERE user_id = $2` means a crafted form cannot reach another
// account's rule even if it guesses an id. The service ALSO intersects the ids
// with the rows it just read, so a stale id is a no-op rather than a surprise.
package db

import (
	"database/sql"
	"fmt"
)

// deviceRulesEnabledChunk caps how many ids travel in one UPDATE. SQLite's
// default host-parameter limit is 999, and a domain card can legitimately hold
// several hundred resolved rows, so the ids are chunked well below it.
const deviceRulesEnabledChunk = 400

// SetDeviceRulesEnabled sets device_rules.enabled on the given rule ids, scoped
// to `userID`, and returns how many rows actually changed.
//
// A row that ALREADY holds the requested value is counted by the driver as
// changed on SQLite (it rewrites the row) — that is why the caller's flash says
// "N rules" from this count and the audit detail names the same number: the two
// must never disagree about what happened.
//
// An empty id list writes nothing and is a nil result, not an error: "nothing to
// do" is a legitimate outcome of a group whose rows were just deleted in another
// tab.
func SetDeviceRulesEnabled(d *sql.DB, userID int64, ids []int, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	value := 0
	if enabled {
		value = 1
	}
	var total int64
	for start := 0; start < len(ids); start += deviceRulesEnabledChunk {
		end := start + deviceRulesEnabledChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, value, userID)
		for _, id := range chunk {
			args = append(args, id)
		}
		// $1 = the new state, $2 = the owner, $3.. = the ids. PlaceholdersRange
		// keeps the numbering right on BOTH dialects (a `?` here would be a
		// PostgreSQL syntax error — see placeholders.go).
		q := `UPDATE device_rules SET enabled = $1
		       WHERE user_id = $2 AND id IN (` + PlaceholdersRange(3, len(chunk)+2) + `)`
		res, err := d.Exec(q, args...)
		if err != nil {
			return total, fmt.Errorf("set device_rules.enabled=%d (user=%d, %d id(s)): %w", value, userID, len(chunk), err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
