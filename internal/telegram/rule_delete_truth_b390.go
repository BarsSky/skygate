// B390 (2026-10-25, TD-33 H6) — A DELETE REPLY MUST COUNT DELETIONS, NOT ITERATIONS.
//
// /delrule and /clearrules both ran
//
//	for _, j := range jobs {
//	    if domain { n, err := db.DeleteRuleOrCascadeByParentDomain(...); if err == nil { cascade += n-1 } }
//	    else      { _ = db.DeleteRuleForUser(...) }
//	    deleted++            // ← unconditional
//	}
//
// so a failed DELETE, a rule that was already gone, and a rule that belonged to
// somebody else were all reported as "deleted". The user was told their rule list was
// empty while the row was still there and still in the ACL, and the audit row carried
// the same inflated number.
//
// One helper now performs the deletion and reports what actually happened:
//
//   - `deleted`  — ids whose DELETE removed rows (the only thing a reply may count);
//   - `cascade`  — the extra /32 rows those deletions took with them (never negative);
//   - `failed`   — ids that removed NOTHING, with the reason (error, or "matched no
//     row", which is a fact about the request, not a success).
package telegram

import (
	"database/sql"
	"fmt"
	"skygate/internal/db"
)

// ruleDeleteJob is one row /delrule or /clearrules intends to delete.
type ruleDeleteJob struct {
	id           int
	targetType   string // "domain" | "subnet" | "ip" | …
	parentDomain string // non-empty only for a CDN/domain rule
}

// ruleDeleteResult is what the deletion actually did.
type ruleDeleteResult struct {
	deleted []int
	cascade int
	failed  []string
}

// deleteRuleJobs performs the deletions and counts real work.
func deleteRuleJobs(d *sql.DB, userID int64, jobs []ruleDeleteJob) ruleDeleteResult {
	var out ruleDeleteResult
	// sql.ErrNoRows is not the only way a DELETE can match nothing: the helpers
	// report it as zero rows affected, which is the honest signal either way.
	note := func(id int, reason string) {
		out.failed = append(out.failed, fmt.Sprintf("#%d (%s)", id, reason))
	}
	for _, j := range jobs {
		if j.targetType == "domain" && j.parentDomain != "" {
			n, err := db.DeleteRuleOrCascadeByParentDomain(d, userID, j.id, j.parentDomain)
			switch {
			case err != nil:
				note(j.id, err.Error())
			case n <= 0:
				note(j.id, "nothing matched")
			default:
				out.deleted = append(out.deleted, j.id)
				if n > 1 {
					out.cascade += int(n) - 1
				}
			}
			continue
		}
		n, err := db.DeleteRuleForUserRows(d, j.id, userID)
		switch {
		case err != nil:
			note(j.id, err.Error())
		case n <= 0:
			note(j.id, "nothing matched")
		default:
			out.deleted = append(out.deleted, j.id)
		}
	}
	return out
}

// failedSuffix renders the "what did not happen" half of a reply, or "" when
// everything the caller asked for was really deleted.
func failedSuffix(list []string) string {
	if len(list) == 0 {
		return ""
	}
	out := ""
	for i, f := range list {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}
