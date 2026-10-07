// device_rules_b358_test.go — B358 (2026-10-06).
//
// The operator's complaint, pinned at the data layer:
//
//	«веб-форма до сих пор странно отображает правила exit rules для администратора
//	 теперь восемь страниц при том что в группе всего три устройства и два
//	 пользователя … пользователи и группы размазаны по этим восьми страницам»
//
// The window unit is the GROUP — a distinct (user_id, device_id) pair. Two properties
// carry the fix:
//
//  1. a group is NEVER split across pages (the rows are fetched by group, not by a
//     row window);
//  2. the totals the page prints come from the DATABASE (COUNT over enabled rules +
//     COUNT over groups), never from the rows the window happens to hold.
//
// The live shape — 2 users, 3 devices, 393 enabled rows (skyadmin/skyworker 219,
// skyadmin/cyborg 12, michail/basic 162) — is asserted to be ONE page at the default
// db.AdminGroupsPerPage, which is what the operator asked for.
package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// b358Seed migrates a fresh SQLite database and returns the seed helper.
func b358Seed(t *testing.T, name string) (*sql.DB, func(string, ...interface{})) {
	t.Helper()
	_, d, err := OpenWithDialect("sqlite:" + t.TempDir() + "/" + name + ".db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return d, func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
}

// b358SeedGroup inserts n enabled subnet rules for one (user, device) group. The
// target values differ per row so the natural-key unique index never collapses them.
func b358SeedGroup(t *testing.T, d *sql.DB, userID int64, deviceID int, hostname string, secondOctet, n int) {
	t.Helper()
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO device_rules
		(user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, parent_domain, action, enabled)
		VALUES (?, ?, ?, ?, 'karolina', 'subnet', ?, '', 'accept', 1)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < n; i++ {
		value := fmt.Sprintf("10.%d.%d.0/24", secondOctet, i)
		if _, err := stmt.Exec(userID, deviceID, hostname, "owner", value); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestAdminRuleGroupsPaged_WholeGroupsAndDatabaseTotals_B358 is the core contract:
// two pages of two and one group, with no rule on both pages and every counter taken
// from the database.
func TestAdminRuleGroupsPaged_WholeGroupsAndDatabaseTotals_B358(t *testing.T) {
	d, mustExec := b358Seed(t, "b358core")
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES
	          (1, 'skyadmin', 'x', 1), (6, 'michail', 'x', 0)`)
	b358SeedGroup(t, d, 1, 9, "skyworker", 0, 25)
	b358SeedGroup(t, d, 1, 56, "cyborg", 1, 3)
	b358SeedGroup(t, d, 6, 29, "basic", 2, 18)
	// A DISABLED row: it is not a group, it is not a row, it is not a total.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled)
	          VALUES (6, 29, 'basic', 'michail', 'karolina', 'subnet', '203.0.113.0/24', 'accept', 0)`)

	page1, err := GetAllRulesForAdminPaged(d, 1, 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	// The totals are the DATABASE's, not this window's 28 rows.
	if page1.TotalRules != 46 {
		t.Errorf("TotalRules = %d, want 46 (25+3+18 enabled; the disabled row must not count, the unwindowed group must)", page1.TotalRules)
	}
	if page1.TotalGroups != 3 {
		t.Errorf("TotalGroups = %d, want 3", page1.TotalGroups)
	}
	if page1.TotalUsers != 2 {
		t.Errorf("TotalUsers = %d, want 2", page1.TotalUsers)
	}
	if page1.GroupsOnPage != 2 {
		t.Errorf("GroupsOnPage = %d, want 2", page1.GroupsOnPage)
	}
	if page1.PageSize != 2 {
		t.Errorf("PageSize = %d, want 2", page1.PageSize)
	}
	if len(page1.Rules) != 28 {
		t.Errorf("page 1 rows = %d, want 28 (25 skyworker + 3 cyborg — whole groups)", len(page1.Rules))
	}
	seen := map[int]int{}
	for _, r := range page1.Rules {
		seen[r.DeviceID]++
	}
	if seen[9] != 25 || seen[56] != 3 {
		t.Errorf("page 1 rows per device = %v, want map[9:25 56:3] — a group must not be split", seen)
	}
	if _, ok := seen[29]; ok {
		t.Errorf("page 1 holds device 29, which belongs to page 2")
	}

	page2, err := GetAllRulesForAdminPaged(d, 2, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2.Rules) != 18 {
		t.Errorf("page 2 rows = %d, want 18 (basic's whole group)", len(page2.Rules))
	}
	for _, r := range page2.Rules {
		if r.DeviceID != 29 {
			t.Errorf("page 2 holds device %d, want only 29", r.DeviceID)
		}
	}
	// Same database totals on every page: the page counter may move, the totals may not.
	if page2.TotalRules != page1.TotalRules || page2.TotalGroups != page1.TotalGroups || page2.TotalUsers != page1.TotalUsers {
		t.Errorf("page 2 totals = (%d rules, %d groups, %d users), want the page 1 totals (%d, %d, %d)",
			page2.TotalRules, page2.TotalGroups, page2.TotalUsers,
			page1.TotalRules, page1.TotalGroups, page1.TotalUsers)
	}
	// No rule ID may appear on both pages: that IS "a group split across pages".
	first := map[int]bool{}
	for _, r := range page1.Rules {
		first[r.ID] = true
	}
	for _, r := range page2.Rules {
		if first[r.ID] {
			t.Errorf("rule %d is rendered on both pages — the group was split", r.ID)
		}
	}

	// A page beyond the end clamps to the last page instead of rendering nothing.
	last, err := GetAllRulesForAdminPaged(d, 99, 2)
	if err != nil {
		t.Fatalf("page 99: %v", err)
	}
	if last.Page != 2 || len(last.Rules) != 18 {
		t.Errorf("page 99 → page %d with %d rows, want page 2 with 18 (clamp to the last page)", last.Page, len(last.Rules))
	}

	// An empty table is an empty page, never a nil slice or an error.
	empty, err := GetAllRulesForAdminPaged(d, 1, 2)
	if err != nil {
		t.Fatalf("empty table: %v", err)
	}
	if empty.Rules == nil {
		t.Errorf("an empty page must return an empty slice, not nil")
	}
}

// TestAdminRuleGroupsPaged_LiveShapeIsOnePage_B358 is the operator's sentence turned
// into an assertion: 2 users and 3 devices must not be EIGHT pages. The live row
// counts (219/12/162) are seeded and the default window is used.
func TestAdminRuleGroupsPaged_LiveShapeIsOnePage_B358(t *testing.T) {
	if AdminGroupsPerPage != 20 {
		t.Fatalf("AdminGroupsPerPage = %d, want 20 (the value this test and the docs quote)", AdminGroupsPerPage)
	}
	d, mustExec := b358Seed(t, "b358live")
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES
	          (1, 'skyadmin', 'x', 1), (6, 'michail', 'x', 0)`)
	b358SeedGroup(t, d, 1, 9, "skyworker", 0, 219)
	b358SeedGroup(t, d, 1, 56, "cyborg", 1, 12)
	b358SeedGroup(t, d, 6, 29, "basic", 2, 162)

	page, err := GetAllRulesForAdminPaged(d, 1, AdminGroupsPerPage)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if page.TotalRules != 393 {
		t.Errorf("TotalRules = %d, want 393 (the live enabled-rule total)", page.TotalRules)
	}
	if page.TotalGroups != 3 {
		t.Errorf("TotalGroups = %d, want 3 devices", page.TotalGroups)
	}
	if page.GroupsOnPage != 3 || len(page.Rules) != 393 {
		t.Errorf("page 1 = %d groups / %d rows, want all 3 groups and all 393 rows on ONE page (the 8-page complaint)",
			page.GroupsOnPage, len(page.Rules))
	}
	perDevice := map[int]int{}
	for _, r := range page.Rules {
		perDevice[r.DeviceID]++
	}
	if perDevice[9] != 219 || perDevice[56] != 12 || perDevice[29] != 162 {
		t.Errorf("rows per device = %v, want map[9:219 29:162 56:12]", perDevice)
	}
}

// TestAdminRulesForGroupsQuery_Placeholders_B358 pins the OR-chain: the placeholders
// are $N (the form both dialects bind by position) and the Go argument order matches
// the predicate order, so a group can never be answered with another group's rows.
func TestAdminRulesForGroupsQuery_Placeholders_B358(t *testing.T) {
	query, args := adminRulesForGroupsQuery([]AdminRuleGroup{
		{UserID: 1, DeviceID: 9},
		{UserID: 6, DeviceID: 29},
	})
	if len(args) != 4 {
		t.Fatalf("args = %v, want 4 values (2 per group)", args)
	}
	if args[0] != int64(1) || args[1] != 9 || args[2] != int64(6) || args[3] != 29 {
		t.Errorf("args = %v, want [1 9 6 29]", args)
	}
	wantPredicate := "(r.user_id = $1 AND r.device_id = $2) OR (r.user_id = $3 AND r.device_id = $4)"
	if !strings.Contains(query, wantPredicate) {
		t.Errorf("predicate missing from the built query:\nwant: %s\n got: %s", wantPredicate, query)
	}
	if strings.Contains(query, "LIMIT") || strings.Contains(query, "OFFSET") {
		t.Errorf("the group-scoped rules fetch must not carry a ROW window:\n%s", query)
	}
}

// TestAdminRuleGroupsPaged_B358_PG runs the same contract on real PostgreSQL — the
// dialect the live deployment runs. The window query carries a derived table, a
// COUNT(DISTINCT …) and a LIMIT/OFFSET; the rules fetch is a dynamically built
// OR-chain over $N placeholders. None of that is exercised by SQLite alone
// (CI sets SKYGATE_TEST_PG_DSN).
func TestAdminRuleGroupsPaged_B358_PG(t *testing.T) {
	d := OpenTestPG(t)
	// The schema is reused between runs, so the fixture starts from a known state.
	if _, err := d.Exec(`DELETE FROM device_rules WHERE device_id IN (90358, 90359)`); err != nil {
		t.Fatalf("clean fixture: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin)
	                     VALUES (4242, 'b358pg', 'x', 0) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := d.Exec(`INSERT INTO device_rules
			(user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, parent_domain, action, enabled)
			VALUES (4242, 90358, 'pgdev-a', 'b358pg', 'karolina', 'subnet', $1, '', 'accept', 1)`,
			fmt.Sprintf("198.51.100.%d/32", i)); err != nil {
			t.Fatalf("seed device 90358 row %d: %v", i, err)
		}
	}
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, parent_domain, action, enabled)
		VALUES (4242, 90359, 'pgdev-b', 'b358pg', 'karolina', 'subnet', '203.0.113.0/32', '', 'accept', 1)`); err != nil {
		t.Fatalf("seed device 90359: %v", err)
	}

	page1, err := GetAllRulesForAdminPaged(d, 1, 1)
	if err != nil {
		t.Fatalf("page 1 on PG: %v", err)
	}
	if page1.GroupsOnPage != 1 || len(page1.Rules) != 3 {
		t.Fatalf("page 1 = %d group(s) / %d row(s), want 1 group with its 3 whole rows", page1.GroupsOnPage, len(page1.Rules))
	}
	if page1.TotalGroups != 2 || page1.TotalRules != 4 || page1.TotalUsers != 1 {
		t.Errorf("totals on PG = (%d groups, %d rules, %d users), want (2, 4, 1)",
			page1.TotalGroups, page1.TotalRules, page1.TotalUsers)
	}
	page2, err := GetAllRulesForAdminPaged(d, 2, 1)
	if err != nil {
		t.Fatalf("page 2 on PG: %v", err)
	}
	if len(page2.Rules) != 1 || page2.Rules[0].DeviceID != 90359 {
		t.Fatalf("page 2 = %+v, want the single row of device 90359 (the other group, whole)", page2.Rules)
	}
	for _, r := range page2.Rules {
		if r.UserName == "" || r.UserName == "?" {
			t.Errorf("user_name = %q — the LEFT JOIN must name the owner", r.UserName)
		}
	}
	if _, err := d.Exec(`DELETE FROM device_rules WHERE device_id IN (90358, 90359)`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
