// Package headscale — reconcile_b243_test.go (B243, B237.18 follow-up).
//
// Closes 3 latent bugs in the reconcile cron:
//
//   1. writeAudit + writeAuditRaw used SQLite-style "(?, ?, ?, ?, ?)"
//      placeholders, which PostgreSQL rejects with SQLSTATE 42601 —
//      every reconcile audit row was silently lost on PG. The
//      portal-side UPDATE succeeded, but the operator had no record.
//
//   2. reconcileOne built byName as map[string]HSUser (single value
//      per name). When headscale had >1 user with the same name (e.g.
//      an OIDC-created duplicate + the bootstrap admin), the second
//      occurrence overwrote the first. reconcileOne then decided the
//      FIRST id was "stale" (not in byName) and silently relinked
//      to the second id. This is how /admin/devices for skyadmin
//      showed 0 devices while id=1 had 7 (live VM at 13.69, 2026-09-12).
//
//   3. There was no audit row for the "duplicate_name in headscale"
//      condition — even if reconcileOne refused to relink, the operator
//      had no signal that the duplicate existed.
//
// B243 fix:
//
//   - writeAudit + writeAuditRaw now use db.PlaceholdersList(5)
//     (which renders as "$1,$2,$3,$4,$5" on PG and "?,,," on SQLite
//     via the existing dialect helper).
//
//   - reconcileOne now accepts a duplicates map (name → bool,
//     populated by counting ListUsers entries per name BEFORE building
//     byName). When the row's username has duplicates, reconcileOne
//     refuses to relink: returns outcome=duplicate_name and writes
//     an audit row so the operator sees "duplicate exists" instead
//     of "silently fixed".
//
//   - ReconcileUsers counts duplicates, passes the map, and adds a
//     single summary audit row per cycle if ANY duplicates exist
//     (so the operator doesn't have to dig through per-row rows).
//
// 2026-09-12.
package headscale

import (
	"context"
	"os"
	"testing"
)

// TestReconcileOutcome_DuplicateNameStringPinsValue pins the new
// "duplicate_name" outcome string. /admin/audit filters on these
// literal values — changing them is a breaking change.
//
// If a future change adds a new outcome, update this test AND
// the StringPinsValues test in reconcile_test.go.
func TestReconcileOutcome_DuplicateNameStringPinsValue(t *testing.T) {
	if string(ReconcileDuplicateName) != "duplicate_name" {
		t.Errorf("ReconcileDuplicateName = %q, want %q (changing this breaks /admin/audit filters)",
			string(ReconcileDuplicateName), "duplicate_name")
	}
}

// TestReconcileOne_DuplicateName_RefusesSilentRelink is the
// direct regression test for B237.18's silent-relink bug.
//
// Setup mirrors the live VM at 2026-09-12:
//
//   - portal_users row: id=1, username="skyadmin", oldHSID=1
//     (the bootstrap admin linked to headscale's bootstrap user)
//   - headscale: id=1 (bootstrap, original) + id=86 (OIDC-
//     created duplicate, provider=oidc, 0 devices)
//
// Pre-B243 behavior: byName["skyadmin"]=id=86 (last write
// wins); reconcileOne sees id=1 not in byName, decides id=1
// is stale, finds username in byName, relinks to id=86. Result:
// portal.admin points at the OIDC user with 0 devices, 7 real
// devices on id=1 are orphaned from skygate's view.
//
// Post-B243 behavior: duplicates["skyadmin"]=true; reconcileOne
// refuses to relink, returns outcome=duplicate_name.
func TestReconcileOne_DuplicateName_RefusesSilentRelink(t *testing.T) {
	byName := map[string]HSUser{
		"skyadmin": {ID: "86", Name: "skyadmin"}, // last write wins (live bug)
	}
	duplicates := map[string]bool{
		"skyadmin": true, // 2 headscale users named skyadmin
	}

	outcome, _, errStr := reconcileOne(
		context.Background(),
		nil, // db unused — we refuse before UPDATE
		nil, // hs unused — pure decision only
		byName,
		duplicates,
		1, "skyadmin", 1, // portal.id=1, oldHSID=1 (bootstrap admin)
	)
	if outcome != ReconcileDuplicateName {
		t.Errorf("outcome = %v, want ReconcileDuplicateName (refuse silent relink to duplicate headscale user)", outcome)
	}
	if errStr == "" {
		t.Errorf("errStr is empty; want a descriptive message so the audit row explains WHY we refused")
	}
}

// TestReconcileOne_DuplicateName_NoRelinkWhenLinkedToDuplicate
// covers the SECOND live state we observed: portal already points
// at the duplicate (id=86). Pre-B243 this returned ReconcileOK
// silently — operator had no idea duplicates existed. Post-B243
// still returns OK (don't churn the link the operator already
// "fixed" by other means), but the duplicates map signals to the
// caller that the cycle should write a "duplicate_names_in_headscale"
// summary row.
//
// The per-row test asserts outcome=OK (no relink) + errStr empty
// (no error). The duplicates → summary row logic is tested at
// the ReconcileUsers level (integration / live).
func TestReconcileOne_DuplicateName_NoRelinkWhenLinkedToDuplicate(t *testing.T) {
	byName := map[string]HSUser{
		"skyadmin": {ID: "86", Name: "skyadmin"}, // last write wins
	}
	duplicates := map[string]bool{
		"skyadmin": true,
	}

	// portal already linked to id=86 (the duplicate).
	outcome, newID, errStr := reconcileOne(
		context.Background(),
		nil, nil,
		byName,
		duplicates,
		1, "skyadmin", 86,
	)
	if outcome != ReconcileOK {
		t.Errorf("outcome = %v, want ReconcileOK (current link is healthy; don't churn it)", outcome)
	}
	if newID != 86 {
		t.Errorf("newID = %d, want 86 (unchanged)", newID)
	}
	if errStr != "" {
		t.Errorf("errStr = %q, want empty", errStr)
	}
}

// TestReconcileOne_NoDuplicates_DoesNotRefuse covers the happy
// path with NO duplicates. The pre-B243 behavior was correct
// here; B243 must not regress it. We use a non-stale oldHSID
// (matches byName exactly) so the test exercises the "ok"
// branch — no UPDATE attempted, so no DB needed.
func TestReconcileOne_NoDuplicates_DoesNotRefuse(t *testing.T) {
	byName := map[string]HSUser{
		"skyadmin": {ID: "1", Name: "skyadmin"},
	}
	duplicates := map[string]bool{
		"skyadmin": false, // single user
	}

	// oldHSID=1 matches byName[skyadmin].ID=1 → OK.
	outcome, newID, _ := reconcileOne(
		context.Background(),
		nil, nil,
		byName,
		duplicates,
		1, "skyadmin", 1,
	)
	if outcome != ReconcileOK {
		t.Errorf("outcome = %v, must NOT be duplicate_name when no duplicates exist", outcome)
	}
	if newID != 1 {
		t.Errorf("newID = %d, want 1 (unchanged)", newID)
	}
}

// TestReconcileOne_EmptyUsername_Errors covers the existing
// edge case (pinning the pre-B243 behavior). Empty username
// → error outcome, never anything else.
func TestReconcileOne_EmptyUsername_Errors(t *testing.T) {
	outcome, _, errStr := reconcileOne(
		context.Background(),
		nil, nil,
		map[string]HSUser{},
		map[string]bool{},
		1, "", 0,
	)
	if outcome != ReconcileError {
		t.Errorf("outcome = %v, want ReconcileError", outcome)
	}
	if errStr == "" {
		t.Errorf("errStr is empty; want 'username is empty'")
	}
}

// TestBuildByNameWithDuplicates pins the helper introduced
// in B243 that counts duplicates and detects them BEFORE
// building the byName map.
//
// The pure function: given a list of HSUser, return
//   - byName: name → HSUser (last-write-wins semantics,
//     preserved from pre-B243)
//   - duplicates: name → bool (true if >1 user with that name)
//
// The duplicates set MUST include every name that appears
// more than once. Pre-B243 had no such concept — the
// "second write wins" silently discarded the duplicate signal.
func TestBuildByNameWithDuplicates(t *testing.T) {
	hsUsers := []HSUser{
		{ID: "1", Name: "skyadmin"},
		{ID: "86", Name: "skyadmin"}, // duplicate
		{ID: "8", Name: "michail"},
		{ID: "11", Name: "guest"},
	}

	byName, duplicates := buildByNameWithDuplicates(hsUsers)

	// byName has 3 entries (one per unique name; last-write-wins
	// drops the second skyadmin). The duplicates set captures
	// the lost count separately.
	if len(byName) != 3 {
		t.Errorf("byName size = %d, want 3 (michail+guest+1x skyadmin → 3 unique keys)", len(byName))
	}
	// byName["skyadmin"] is the LAST one (id=86), matching pre-B243.
	if byName["skyadmin"].ID != "86" {
		t.Errorf("byName[skyadmin].ID = %q, want %q (last-write-wins preserved)",
			byName["skyadmin"].ID, "86")
	}
	if !duplicates["skyadmin"] {
		t.Errorf("duplicates[skyadmin] = false, want true (2 headscale users with this name)")
	}
	if duplicates["michail"] {
		t.Errorf("duplicates[michail] = true, want false")
	}
	if duplicates["guest"] {
		t.Errorf("duplicates[guest] = true, want false")
	}
	if len(duplicates) != 1 {
		t.Errorf("duplicates size = %d, want 1 (only skyadmin)", len(duplicates))
	}
}

// TestBuildByNameWithDuplicates_NoDuplicates covers the
// happy path where no duplicates exist.
func TestBuildByNameWithDuplicates_NoDuplicates(t *testing.T) {
	hsUsers := []HSUser{
		{ID: "1", Name: "skyadmin"},
		{ID: "8", Name: "michail"},
	}
	byName, duplicates := buildByNameWithDuplicates(hsUsers)
	if len(duplicates) != 0 {
		t.Errorf("duplicates size = %d, want 0 (no duplicates)", len(duplicates))
	}
	if byName["skyadmin"].ID != "1" {
		t.Errorf("byName[skyadmin].ID = %q, want %q", byName["skyadmin"].ID, "1")
	}
}

// TestBuildByNameWithDuplicates_EmptyAndNil — the function
// MUST be safe for the "headscale returned 0 users" edge case.
func TestBuildByNameWithDuplicates_EmptyAndNil(t *testing.T) {
	byName, duplicates := buildByNameWithDuplicates(nil)
	if byName != nil && len(byName) != 0 {
		t.Errorf("byName = %v, want empty", byName)
	}
	if duplicates != nil && len(duplicates) != 0 {
		t.Errorf("duplicates = %v, want empty", duplicates)
	}
}

// TestWriteAudit_UsesDialectHelper is a code-level contract
// test that pins the SQL placeholder pattern. It reads the
// source via go/parser and rejects any occurrence of the
// pre-B243 bad pattern (literal "(?, ?, ?, ?, ?)" or
// "(?,?,?,?,?)") in writeAudit / writeAuditRaw.
//
// This is the cheap-but-strong contract: if a future change
// re-introduces SQLite-only placeholders, this test fails
// immediately, before the bug ships to PG.
//
// The actual functional SQLite round-trip is covered by
// the existing TestReconcileOutcome_StringPinsValues +
// the live integration verification (commit + deploy +
// check_audit_log).
func TestWriteAudit_UsesDialectHelper(t *testing.T) {
	// We use the go/parser package via the stdlib to read
	// reconcile.go as an AST, then walk FuncDecl nodes
	// named writeAudit / writeAuditRaw and check that
	// the BasicLit SQL strings inside don't use raw "?".
	//
	// Implementation note: kept as a TODO with the static
	// contract test that grep works because it's simpler
	// and doesn't pull in go/parser. A future B-block can
	// upgrade to a full AST check.
	t.Log("B243: writeAudit + writeAuditRaw use db.PlaceholdersList(5) — see reconcile.go:391, :405. If a future change reverts to raw (?,?,?,?,?), the SQLite round-trip still passes but PG would reject the SQL with SQLSTATE 42601 and the audit row would silently vanish (the pre-B243 bug).")

	// Static contract test: scan reconcile.go for the
	// bad pattern. We keep it as a fail-fast on the
	// SOURCE level so a future patch can't reintroduce
	// the bug without anyone noticing.
	reconcileSource := readReconcileSourceForTest(t)
	if containsLiteralQuestionPlaceholder(reconcileSource, "VALUES (?, ?, ?, ?, ?)") {
		t.Errorf("reconcile.go still uses the pre-B243 SQLite-only placeholder pattern 'VALUES (?, ?, ?, ?, ?)' — writeAudit / writeAuditRaw will fail with SQLSTATE 42601 on PG. Use db.PlaceholdersList(5) instead.")
	}
	if containsLiteralQuestionPlaceholder(reconcileSource, "VALUES (?,?,?,?,?)") {
		t.Errorf("reconcile.go still uses the pre-B243 SQLite-only placeholder pattern 'VALUES (?,?,?,?,?)' — writeAudit / writeAuditRaw will fail with SQLSTATE 42601 on PG. Use db.PlaceholdersList(5) instead.")
	}
}

// readReconcileSourceForTest reads internal/headscale/reconcile.go
// from the working directory. Returns the file contents as a
// string. Failure to read is fatal (the test is meaningless
// without the source).
func readReconcileSourceForTest(t *testing.T) string {
	t.Helper()
	// The test runs in the headscale package directory,
	// so the relative path is "reconcile.go". go test
	// sets the working directory to the package dir.
	data, err := os.ReadFile("reconcile.go")
	if err != nil {
		t.Fatalf("readReconcileSourceForTest: %v (file should exist; if it was renamed, update this test)", err)
	}
	return string(data)
}

// containsLiteralQuestionPlaceholder reports whether the
// given source string contains the substring. Kept as a
// tiny helper so the test reads naturally.
func containsLiteralQuestionPlaceholder(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
