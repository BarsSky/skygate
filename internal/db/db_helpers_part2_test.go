package db

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
)

// 2026-07-11: Этап 9 part 2 — tests for audit_log + device_rules helpers.
// Pattern matches Этап 9 (db_helpers_test.go): openTestDB() returns a
// fresh sqlite with the full migration chain applied, so the helpers
// are exercised against the real schema, not a mock.

// --- audit_log ---

func TestAppendAuditLog(t *testing.T) {
	d := openTestDB(t)

	if err := AppendAuditLog(d, 1, "skyadmin", "user_create", "created alice"); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	if err := AppendAuditLog(d, 0, "telegram", "telegram_ack", "alert_id=5"); err != nil {
		t.Fatalf("AppendAuditLog telegram: %v", err)
	}

	// Read back via ListAuditActions to confirm both rows landed and
	// the action filter returns both values.
	actions, err := ListAuditActions(d)
	if err != nil {
		t.Fatalf("ListAuditActions: %v", err)
	}
	sort.Strings(actions)
	want := []string{"telegram_ack", "user_create"}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Errorf("actions[%d] = %q, want %q", i, actions[i], want[i])
		}
	}
}

func TestAppendAuditLogNoUser(t *testing.T) {
	d := openTestDB(t)

	if err := AppendAuditLogNoUser(d, "telegram", "telegram_restart", "token=abc123"); err != nil {
		t.Fatalf("AppendAuditLogNoUser: %v", err)
	}

	// The 3-col variant must NOT touch user_id. Read the raw row to verify.
	var userID int64
	var username, action, detail string
	err := d.QueryRow(
		`SELECT user_id, username, action, detail FROM audit_log WHERE action = 'telegram_restart'`,
	).Scan(&userID, &username, &action, &detail)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if userID != 0 {
		t.Errorf("user_id = %d, want 0 (column absent from INSERT)", userID)
	}
	if username != "telegram" || action != "telegram_restart" || detail != "token=abc123" {
		t.Errorf("got (%q, %q, %q), want (telegram, telegram_restart, token=abc123)", username, action, detail)
	}
}

func TestDeleteAuditLogByUserID(t *testing.T) {
	d := openTestDB(t)

	// Two users, three rows total. Delete user 1's rows; user 2 stays.
	if err := AppendAuditLog(d, 1, "skyadmin", "user_create", "x"); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := AppendAuditLog(d, 1, "skyadmin", "rule_add", "y"); err != nil {
		t.Fatalf("append 1b: %v", err)
	}
	if err := AppendAuditLog(d, 2, "michail", "login", "z"); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	if err := DeleteAuditLogByUserID(d, 1); err != nil {
		t.Fatalf("DeleteAuditLogByUserID: %v", err)
	}

	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Errorf("rows after delete = %d, want 1 (only michail's)", left)
	}

	// Idempotent: deleting again is a no-op, not an error.
	if err := DeleteAuditLogByUserID(d, 1); err != nil {
		t.Errorf("second delete should be no-op, got %v", err)
	}
}

func TestListAuditActionsDistinct(t *testing.T) {
	d := openTestDB(t)

	// Insert duplicates. ListAuditActions must return each action once.
	for i := 0; i < 3; i++ {
		if err := AppendAuditLog(d, 1, "skyadmin", "login", ""); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := AppendAuditLog(d, 1, "skyadmin", "logout", ""); err != nil {
		t.Fatalf("append logout: %v", err)
	}

	actions, err := ListAuditActions(d)
	if err != nil {
		t.Fatalf("ListAuditActions: %v", err)
	}
	// Expect exactly 2: login, logout (sorted alphabetically).
	if len(actions) != 2 || actions[0] != "login" || actions[1] != "logout" {
		t.Errorf("actions = %v, want [login logout]", actions)
	}
}

// --- device_rules ---

// insertRule is a tiny test-only helper that mirrors the App.insertRuleUnique
// pattern (FindDeviceRuleID + AppendDeviceRule) without pulling in the
// handler package. Tests use it to seed data. It also inserts a
// portal_users row for the user_id so the device_rules FK (added in
// V020) is satisfied — the live app inserts portal_users via
// bootstrapAdmin, but the test DB starts empty.
func insertRule(t *testing.T, d *sql.DB, userID, deviceID int, exitNode, tt, tv, action, ip, parent string) int64 {
	t.Helper()
	// Ensure the FK target exists. Use INSERT OR IGNORE so re-runs
	// (and tests that use the same user_id twice) don't trip on
	// UNIQUE(username).
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO portal_users (id, username, password_hash, is_admin) VALUES (?, ?, 'x', 0)`,
		userID, "user"+string(rune('0'+userID)),
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	id, err := AppendDeviceRule(d, int64(userID), deviceID, exitNode, tt, tv, action, ip, parent)
	if err != nil {
		t.Fatalf("AppendDeviceRule: %v", err)
	}
	return id
}

func TestAppendAndFindDeviceRule(t *testing.T) {
	d := openTestDB(t)

	id := insertRule(t, d, 1, 8, "karolina", "domain", "example.com", "accept", "100.64.0.1", "example.com")
	if id <= 0 {
		t.Fatalf("AppendDeviceRule returned id=%d, want > 0", id)
	}

	// FindDeviceRuleID should locate the just-inserted row.
	gotID, err := FindDeviceRuleID(d, 1, 8, "karolina", "domain", "example.com")
	if err != nil {
		t.Fatalf("FindDeviceRuleID: %v", err)
	}
	if gotID != int(id) {
		t.Errorf("FindDeviceRuleID = %d, want %d", gotID, id)
	}
}

func TestFindDeviceRuleID_NotFound(t *testing.T) {
	d := openTestDB(t)

	// Empty DB — must return ErrNotFound, not sql.ErrNoRows.
	_, err := FindDeviceRuleID(d, 1, 8, "karolina", "domain", "nope.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound (so callers can use errors.Is)", err)
	}
}

func TestFindDomainRuleID(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "example.com", "accept", "", "example.com")

	// Same (user, device, exit_node, domain) tuple, different target_type
	// (e.g. an IP) — FindDomainRuleID should NOT match it.
	insertRule(t, d, 1, 8, "karolina", "ip", "1.2.3.4", "accept", "", "")

	gotID, err := FindDomainRuleID(d, 1, 8, "karolina", "example.com")
	if err != nil {
		t.Fatalf("FindDomainRuleID: %v", err)
	}
	if gotID <= 0 {
		t.Errorf("got id %d, want > 0", gotID)
	}

	// Wrong target_value → ErrNotFound.
	_, err = FindDomainRuleID(d, 1, 8, "karolina", "other.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong-domain err = %v, want ErrNotFound", err)
	}
}

func TestCountEnabledRules(t *testing.T) {
	d := openTestDB(t)

	// Empty DB → 0.
	n, err := CountEnabledRules(d)
	if err != nil || n != 0 {
		t.Errorf("empty count = %d, %v; want 0, nil", n, err)
	}

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 1, 9, "karolina", "domain", "b.com", "accept", "", "b.com")
	insertRule(t, d, 2, 6, "emilia", "ip", "1.1.1.1", "accept", "", "")

	if n, _ := CountEnabledRules(d); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
}

func TestCountEnabledRulesForDevice(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "accept", "", "")
	insertRule(t, d, 1, 9, "karolina", "domain", "b.com", "accept", "", "b.com")

	if n, _ := CountEnabledRulesForDevice(d, 8); n != 2 {
		t.Errorf("device 8 count = %d, want 2", n)
	}
	if n, _ := CountEnabledRulesForDevice(d, 9); n != 1 {
		t.Errorf("device 9 count = %d, want 1", n)
	}
	if n, _ := CountEnabledRulesForDevice(d, 999); n != 0 {
		t.Errorf("device 999 count = %d, want 0", n)
	}
}

func TestCountEnabledNonSubnetRulesForUser(t *testing.T) {
	d := openTestDB(t)

	// Domain rule → counts (parent_domain == target_value, not a /32).
	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	// Subnet /32 WITH parent_domain → does NOT count (it's derived).
	insertRule(t, d, 1, 8, "karolina", "subnet", "1.2.3.4/32", "accept", "", "a.com")
	// Subnet /32 WITHOUT parent_domain → counts (orphaned /32, e.g. legacy).
	insertRule(t, d, 1, 8, "karolina", "subnet", "5.6.7.8/32", "accept", "", "")
	// IP rule → counts.
	insertRule(t, d, 1, 8, "karolina", "ip", "9.10.11.12", "accept", "", "")

	n, err := CountEnabledNonSubnetRulesForUser(d, 1)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// 4 total minus 1 (the /32 with parent_domain) = 3.
	if n != 3 {
		t.Errorf("non-subnet count = %d, want 3", n)
	}
}

func TestCountRulesByDeviceForUser(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "accept", "", "")
	insertRule(t, d, 1, 9, "karolina", "domain", "b.com", "accept", "", "b.com")
	// Derived /32 — must NOT count.
	insertRule(t, d, 1, 9, "karolina", "subnet", "1.2.3.4/32", "accept", "", "b.com")

	m, err := CountRulesByDeviceForUser(d, 1)
	if err != nil {
		t.Fatalf("CountRulesByDeviceForUser: %v", err)
	}
	if m[8] != 2 {
		t.Errorf("device 8 = %d, want 2", m[8])
	}
	if m[9] != 1 {
		t.Errorf("device 9 = %d, want 1 (subnet /32 with parent excluded)", m[9])
	}
}

func TestGetDeviceRulesForUser(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 2, 6, "emilia", "ip", "1.1.1.1", "accept", "", "")

	rules, err := GetDeviceRulesForUser(d, 1)
	if err != nil {
		t.Fatalf("GetDeviceRulesForUser: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1 (only user 1's)", len(rules))
	}
	r := rules[0]
	if r.UserID != 1 || r.DeviceID != 8 || r.ExitNodeID != "karolina" ||
		r.TargetType != "domain" || r.TargetValue != "a.com" || !r.Enabled {
		t.Errorf("rule = %+v, want user=1 device=8 exit=karolina domain a.com enabled", r)
	}
}

func TestGetAllRulesForAdmin(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 2, 6, "emilia", "ip", "1.1.1.1", "accept", "", "")

	rules, err := GetAllRulesForAdmin(d)
	if err != nil {
		t.Fatalf("GetAllRulesForAdmin: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d, want 2", len(rules))
	}
	// UserName comes from the LEFT JOIN onto portal_users. The
	// insertRule helper seeds a portal_users row for each user_id,
	// so the JOIN matches and the username ("user1" / "user2")
	// comes through.
	got := map[int]string{}
	for _, r := range rules {
		got[r.UserID] = r.UserName
	}
	if got[1] != "user1" || got[2] != "user2" {
		t.Errorf("UserName mapping = %v, want user1/user2", got)
	}
}

func TestGetACLEntries(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "100.64.0.1", "a.com")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "deny", "100.64.0.1", "")

	entries, err := GetACLEntries(d)
	if err != nil {
		t.Fatalf("GetACLEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// First row: domain a.com, action accept, device_ip 100.64.0.1.
	if entries[0].TargetType != "domain" || entries[0].Action != "accept" || entries[0].DeviceIP != "100.64.0.1" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
}

func TestGetEnabledDomainRules(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "accept", "", "")

	dom, err := GetEnabledDomainRules(d)
	if err != nil {
		t.Fatalf("GetEnabledDomainRules: %v", err)
	}
	if len(dom) != 1 {
		t.Fatalf("got %d domain rules, want 1", len(dom))
	}
	if dom[0].Target != "a.com" {
		t.Errorf("target = %q, want a.com", dom[0].Target)
	}
}

func TestGetEnabledSubnetIPRules(t *testing.T) {
	d := openTestDB(t)

	insertRule(t, d, 1, 8, "karolina", "subnet", "10.0.0.0/8", "accept", "", "")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "accept", "", "")
	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com") // excluded

	rules, err := GetEnabledSubnetIPRules(d)
	if err != nil {
		t.Fatalf("GetEnabledSubnetIPRules: %v", err)
	}
	if len(rules) != 2 {
		t.Errorf("got %d, want 2 (subnet + ip; domain excluded)", len(rules))
	}
}

func TestListDistinctExitNodesWithRules(t *testing.T) {
	d := openTestDB(t)

	// Two rules on karolina, one on emilia — distinct should be 2.
	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	insertRule(t, d, 1, 8, "karolina", "ip", "1.1.1.1", "accept", "", "")
	insertRule(t, d, 1, 9, "emilia", "ip", "2.2.2.2", "accept", "", "")

	list, err := ListDistinctExitNodesWithRules(d)
	if err != nil {
		t.Fatalf("ListDistinctExitNodesWithRules: %v", err)
	}
	sort.Strings(list)
	if len(list) != 2 || list[0] != "emilia" || list[1] != "karolina" {
		t.Errorf("got %v, want [emilia karolina]", list)
	}
}

func TestDeleteRuleAndDeleteRuleForUser(t *testing.T) {
	d := openTestDB(t)

	id1 := insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	id2 := insertRule(t, d, 2, 6, "emilia", "ip", "1.1.1.1", "accept", "", "")

	// DeleteRuleForUser with WRONG userID is a no-op (still returns nil).
	if err := DeleteRuleForUser(d, int(id1), 2); err != nil {
		t.Fatalf("cross-user delete: %v", err)
	}
	if n, _ := CountEnabledRules(d); n != 2 {
		t.Errorf("after cross-user delete count = %d, want 2 (no change)", n)
	}

	// DeleteRuleForUser with correct userID removes the row.
	if err := DeleteRuleForUser(d, int(id1), 1); err != nil {
		t.Fatalf("DeleteRuleForUser: %v", err)
	}
	if n, _ := CountEnabledRules(d); n != 1 {
		t.Errorf("after delete count = %d, want 1", n)
	}

	// DeleteRule (no user check) removes user 2's row too.
	if err := DeleteRule(d, int(id2)); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if n, _ := CountEnabledRules(d); n != 0 {
		t.Errorf("after both deletes count = %d, want 0", n)
	}
}

func TestDeleteRuleOrCascadeByParentDomain(t *testing.T) {
	d := openTestDB(t)

	// Domain rule + two derived /32 rules with parent_domain = a.com.
	insertRule(t, d, 1, 8, "karolina", "domain", "a.com", "accept", "", "a.com")
	id32a := insertRule(t, d, 1, 8, "karolina", "subnet", "1.1.1.1/32", "accept", "", "a.com")
	id32b := insertRule(t, d, 1, 8, "karolina", "subnet", "1.1.1.2/32", "accept", "", "a.com")
	// Unrelated /32 with a different parent — must NOT be removed.
	insertRule(t, d, 1, 8, "karolina", "subnet", "2.2.2.2/32", "accept", "", "b.com")

	if _, err := DeleteRuleOrCascadeByParentDomain(d, 1, int(id32a), "a.com"); err != nil {
		t.Fatalf("DeleteRuleOrCascadeByParentDomain: %v", err)
	}

	// Cascade should have removed id32a + id32b (both have parent=a.com).
	// The b.com /32 and the domain rule should still be present.
	if n, _ := CountEnabledRules(d); n != 2 {
		t.Errorf("count after cascade = %d, want 2 (domain + b.com /32)", n)
	}

	// Verify the right rows survived.
	rules, _ := GetAllRulesForAdmin(d)
	for _, r := range rules {
		if r.ID == int(id32a) || r.ID == int(id32b) {
			t.Errorf("cascade kept id=%d, want it gone", r.ID)
		}
	}
}
