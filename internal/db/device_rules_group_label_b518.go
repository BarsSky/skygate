// device_rules_group_label_b518.go — B518: setting the operator's own group
// label on a CHOSEN set of rules.
//
// B380 shipped the label and two ways to write it: at creation (single form, batch
// form, JSON `group`) and per WHOLE group via rename (`SetDeviceRuleGroup`). That
// leaves the most useful verb out: «these existing rules are one group now» —
// which is exactly how an operator reorganises a page they already have. Re-adding
// the rules to file them under a name is not an option: the natural key would make
// the insert a duplicate, and a duplicate changes nothing (B123/B183), so the label
// would never land.
//
// Owner-scoped like every other writer here, and deliberately free of routing
// consequences: the label never reaches GenerateACL, prefix_owner, the routed
// prefix set or the exit-node choice (the B380 property, re-asserted by the B518
// contract).
package db

import (
	"database/sql"
	"fmt"
)

// deviceRulesGroupLabelChunk caps how many ids travel in one UPDATE (SQLite's
// default host-parameter limit is 999; a domain card can hold hundreds of rows).
const deviceRulesGroupLabelChunk = 400

// SetDeviceRulesGroupLabel writes `label` (already normalised by the caller, "" =
// remove from the group) onto the given rule ids, scoped to `userID`, and returns
// how many rows changed.
func SetDeviceRulesGroupLabel(d *sql.DB, userID int64, ids []int, label string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var total int64
	for start := 0; start < len(ids); start += deviceRulesGroupLabelChunk {
		end := start + deviceRulesGroupLabelChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := make([]any, 0, len(chunk)+2)
		args = append(args, label, userID)
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `UPDATE device_rules SET group_label = $1
		       WHERE user_id = $2 AND id IN (` + PlaceholdersRange(3, len(chunk)+2) + `)`
		res, err := d.Exec(q, args...)
		if err != nil {
			return total, fmt.Errorf("set device_rules.group_label=%q (user=%d, %d id(s)): %w", label, userID, len(chunk), err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
