// acl_set.go — push an operator-supplied policy verbatim.
//
// Split out of acl.go in refactor Phase D (2026-10-01). SetACLForAllPlanes is
// the /admin/headscale ACL import path: it applies text the operator pasted
// rather than anything this package generated, which is why it does not belong
// next to the pipeline that also writes a snapshot and an audit row.

package acl

import (
	"database/sql"
	"fmt"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// SetACLForAllPlanes pushes a PRE-BUILT policy (e.g. one
// loaded from disk by /admin/acls/import) to every plane
// and writes an acl_snapshots row. Skips the GenerateACL
// step — the caller already has the JSON.
//
// 2026-07-16: v0.13.0 — ACL import/export. The dry-run page
// shows the imported policy next to the current one; when
// the operator clicks "Apply", this function pushes it to
// every plane in one go.
func SetACLForAllPlanes(d *sql.DB, hsForPlane func(planeURL string) *headscale.Client, alerter Alerter, username, detailForLog, policy string) []ApplyResult {
	planes, err := db.ListControlPlanes(d)
	if err != nil {
		return []ApplyResult{{Version: 0, Applied: false, Err: fmt.Errorf("list control planes: %w", err)}}
	}
	out := make([]ApplyResult, 0, len(planes))
	for _, p := range planes {
		hs := hsForPlane(p.URL)
		if hs == nil {
			out = append(out, ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("no headscale client for plane %q", p.URL)})
			continue
		}
		// Save snapshot (always, so the operator can roll
		// back even on failure).
		ver := SaveACLSnapshot(d, policy, username, alerter)
		if setErr := hs.SetPolicy(policy); setErr != nil {
			db.MarkACLFail(d, ver, setErr.Error())
			db.AppendExitRuleLog(d, ver, db.ExitRuleActionApplyFail, detailForLog+": "+setErr.Error())
			out = append(out, ApplyResult{Version: ver, Applied: false, Err: setErr})
			continue
		}
		db.MarkACLApplied(d, ver)
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply, detailForLog)
		out = append(out, ApplyResult{Version: ver, Applied: true, Err: nil})
	}
	return out
}
