package db

import (
	"path/filepath"
	"testing"
)

// TestListAuditLogForUser — v0.25.1 unit test for the
// per-user audit log export query. Verifies that the
// (user_id, username) OR-fallback returns BOTH
// user-owned rows AND system events for that username
// (e.g. /ack, /restart that the bot records with
// user_id=0, username="skyadmin").
func TestListAuditLogForUser(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "audit-export-test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Seed: 3 rows for user_id=1, username=skyadmin;
	// 2 rows for user_id=0, username=skyadmin (system
	// events on the user's behalf); 1 row for user_id=2,
	// username=michail (a different user — must NOT
	// appear in skyadmin's export).
	if err := AppendAuditLog(d, 1, "skyadmin", "login_ok", ""); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	if err := AppendAuditLog(d, 1, "skyadmin", "subnet_provision", "user_id=1"); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	if err := AppendAuditLog(d, 1, "skyadmin", "preauth_issued", "1h single-use"); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	if err := AppendAuditLogNoUser(d, "skyadmin", "telegram_restart", "by user 1"); err != nil {
		t.Fatalf("AppendAuditLogNoUser: %v", err)
	}
	if err := AppendAuditLogNoUser(d, "skyadmin", "telegram_ack", "alert_id=42"); err != nil {
		t.Fatalf("AppendAuditLogNoUser: %v", err)
	}
	if err := AppendAuditLog(d, 2, "michail", "login_ok", ""); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}

	// Export for skyadmin (user_id=1, username=skyadmin)
	// — should return ALL 5 rows (3 user-owned + 2 system).
	rows, err := ListAuditLogForUser(d, 1, "skyadmin", 0, 100, 0)
	if err != nil {
		t.Fatalf("ListAuditLogForUser: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("len(rows) = %d, want 5 (3 user + 2 system)", len(rows))
	}
	for _, r := range rows {
		if r.Username != "skyadmin" {
			t.Errorf("row username = %q, want skyadmin (no cross-user leak)", r.Username)
		}
	}

	// Export for michail — only 1 row.
	rows2, err := ListAuditLogForUser(d, 2, "michail", 0, 100, 0)
	if err != nil {
		t.Fatalf("ListAuditLogForUser (michail): %v", err)
	}
	if len(rows2) != 1 {
		t.Errorf("len(rows) for michail = %d, want 1", len(rows2))
	}

	// limit=2 — only 2 rows.
	rows3, err := ListAuditLogForUser(d, 1, "skyadmin", 0, 2, 0)
	if err != nil {
		t.Fatalf("ListAuditLogForUser (limit=2): %v", err)
	}
	if len(rows3) != 2 {
		t.Errorf("len(rows) with limit=2 = %d, want 2", len(rows3))
	}

	// offset=3 — last 2 of 5.
	rows4, err := ListAuditLogForUser(d, 1, "skyadmin", 0, 100, 3)
	if err != nil {
		t.Fatalf("ListAuditLogForUser (offset=3): %v", err)
	}
	if len(rows4) != 2 {
		t.Errorf("len(rows) with offset=3 = %d, want 2 (5 - 3)", len(rows4))
	}

	// since=now-1h (everything) and since=now+1h (nothing)
	// — use the table's created_at. The rows we just
	// inserted are within the last second, so future
	// timestamps filter them out.
	now := rows[0].CreatedAt.Unix()
	rows5, _ := ListAuditLogForUser(d, 1, "skyadmin", now+3600, 100, 0)
	if len(rows5) != 0 {
		t.Errorf("rows with future since = %d, want 0", len(rows5))
	}
	rows6, _ := ListAuditLogForUser(d, 1, "skyadmin", now-3600, 100, 0)
	if len(rows6) != 5 {
		t.Errorf("rows with past since = %d, want 5", len(rows6))
	}
}
