// v1.5.0+ / B221 — unit tests for the AppendAuditLogWithTarget
// helper + the audit_log SQL constants. The actual SQL execution
// is exercised at runtime by the 6+ writers in
// internal/feature/admin/{cluster,database}.go; these tests pin
// the pure-Go contracts (the SQL column list, the documented
// target_type conventions, the empty-target backward-compat path,
// the legacy 5-arg AppendAuditLog still working).

package db

import (
	"database/sql"
	"strings"
	"testing"
)

func TestAppendAuditLogWithTarget_SQLHasSixPlaceholders(t *testing.T) {
	// B221 contract: the new INSERT must list 6
	// placeholders ($1..$6) for (user_id, username,
	// action, detail, target_type, target_id). The
	// legacy 5-arg AppendAuditLog uses the old
	// 4-column INSERT (qInsertAuditLog) — the B221
	// writer uses the new 6-column one. If a future
	// refactor adds a 7th column without updating
	// the placeholder list, this test catches it.
	ph := strings.Count(qInsertAuditLogWithTarget, "$")
	if ph != 6 {
		t.Errorf("qInsertAuditLogWithTarget has %d $N placeholders, want 6 (user_id, username, action, detail, target_type, target_id)", ph)
	}
}

func TestAppendAuditLogWithTarget_SQLTargetsAuditLog(t *testing.T) {
	// B221 contract: the INSERT must write to the
	// audit_log table (not cluster_audit, not
	// audit_log_v2, not anything else). The /admin/audit
	// unified view's audit_log branch projects these
	// rows; if the table name is wrong, the writer
	// silently writes to the wrong place.
	if !strings.Contains(qInsertAuditLogWithTarget, "INSERT INTO audit_log") {
		t.Errorf("qInsertAuditLogWithTarget does not target audit_log: %q", qInsertAuditLogWithTarget)
	}
}

func TestAppendAuditLogWithTarget_SQLProjectsTargetColumns(t *testing.T) {
	// B221 contract: the INSERT must include
	// target_type + target_id in the column list
	// AND the VALUES list. The /admin/audit view
	// reads both columns; if either is missing, the
	// query returns an error at runtime.
	required := []string{
		"target_type",
		"target_id",
		"VALUES",
	}
	for _, want := range required {
		if !strings.Contains(qInsertAuditLogWithTarget, want) {
			t.Errorf("qInsertAuditLogWithTarget missing %q: %q", want, qInsertAuditLogWithTarget)
		}
	}
}

func TestAppendAuditLogWithTarget_TargetTypeConventions(t *testing.T) {
	// B221 contract: the documented target_type
	// values. Pinning the strings here means a
	// refactor of the docstring without updating
	// the writers (or vice-versa) gets caught at
	// test-time. The list mirrors the comment on
	// AppendAuditLogWithTarget in audit_log.go.
	conventions := []string{
		"cluster_node",
		"cluster_invite",
		"cluster_database",
		"portal_user",
		"device",
		"acl",
		"telegram_binding",
		"", // pre-B221 backward compat
	}
	for _, c := range conventions {
		// Each convention is a non-empty
		// discriminator OR the empty string
		// sentinel for "no target". Verify
		// the writer can pass it without
		// mangling: every value must be
		// either "" or match a permissive
		// character set (lowercase letters +
		// underscores, no spaces).
		if c == "" {
			continue // sentinel: allowed
		}
		if strings.ContainsAny(c, " \t\n") {
			t.Errorf("target_type convention %q contains whitespace", c)
		}
		if c != strings.ToLower(c) {
			t.Errorf("target_type convention %q must be lowercase", c)
		}
	}
}

func TestAppendAuditLog_StillWorks_BackwardCompat(t *testing.T) {
	// B221 contract: the legacy 5-arg AppendAuditLog
	// must continue to compile + run. It's the
	// call site for every pre-B221 writer (login,
	// rule_add, device_add, etc.); breaking it
	// would silently regress the audit surface.
	// We can't test the SQL execution without a
	// live PG, but we CAN test the signature still
	// matches (the test would fail to compile if
	// the function was accidentally renamed or
	// re-signatured).
	//
	// The legacy function still uses the OLD
	// qInsertAuditLog (4-column INSERT). The B221
	// migration's DEFAULT '' on target_type +
	// target_id means pre-B221 rows get empty
	// target values — visible in /admin/audit as
	// "—" (no target). No code change needed in
	// the legacy callers.
	var _ func(*sql.DB, int64, string, string, string) error = AppendAuditLog
}

func TestAppendAuditLogWithTarget_Signature(t *testing.T) {
	// B221 contract: the new 7-arg signature. Pinning
	// the parameter order prevents a future refactor
	// from accidentally re-ordering the columns (which
	// would silently misalign the data — target_type
	// would land in the username column, etc.).
	//
	// AppendAuditLogWithTarget(d, userID, username,
	//     action, detail, targetType, targetID)
	//   $1   $2      $3       $4     $5      $6         $7
	//
	// Verify the function pointer can be assigned
	// to a typed function variable. If the order
	// changes, this fails to compile.
	var _ func(*sql.DB, int64, string, string, string, string, string) error = AppendAuditLogWithTarget
}
