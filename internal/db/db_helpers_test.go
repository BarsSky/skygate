package db

import (
	"testing"
)

// 2026-07-11: refactor v0.6.0 (Этап 9) — Этап 9 added AppendExitRuleLog and
// the acl_snapshots helpers. These tests exercise the new code paths against
// a real SQLite database (openTestDB → calls Open() → runs the full migration
// chain). The goal is to lock down the SQL behaviour so a future column
// change to exit_rule_logs / acl_snapshots can't silently break the audit
// log or the rollback flow.

func TestAppendExitRuleLog(t *testing.T) {
	d := openTestDB(t)

	if err := AppendExitRuleLog(d, 7, ExitRuleActionApply, "user a added rule 1"); err != nil {
		t.Fatalf("AppendExitRuleLog: %v", err)
	}
	if err := AppendExitRuleLog(d, 7, ExitRuleActionDelete, "user a deleted rule 1"); err != nil {
		t.Fatalf("AppendExitRuleLog delete: %v", err)
	}
	if err := AppendExitRuleLog(d, ExitRuleLogNoVersion, ExitRuleActionAutoupdate, "domain=example.com added=3 removed=0"); err != nil {
		t.Fatalf("AppendExitRuleLog autoupdate: %v", err)
	}

	rows, err := d.Query(qSelectRecentExitRuleLogs)
	if err != nil {
		t.Fatalf("select logs: %v", err)
	}
	defer rows.Close()
	var got []ExitRuleLog
	for rows.Next() {
		var l ExitRuleLog
		if err := rows.Scan(&l.Version, &l.Action, &l.Detail, &l.CreatedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, l)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 logs, got %d", len(got))
	}
	// Newest first (ORDER BY id DESC)
	if got[0].Action != ExitRuleActionAutoupdate {
		t.Errorf("got[0].Action = %q, want %q", got[0].Action, ExitRuleActionAutoupdate)
	}
	if got[0].Version != ExitRuleLogNoVersion {
		t.Errorf("got[0].Version = %d, want 0 (autoupdate)", got[0].Version)
	}
	if got[1].Action != ExitRuleActionDelete {
		t.Errorf("got[1].Action = %q, want %q", got[1].Action, ExitRuleActionDelete)
	}
	if got[2].Action != ExitRuleActionApply {
		t.Errorf("got[2].Action = %q, want %q", got[2].Action, ExitRuleActionApply)
	}
}

func TestLastSyncForExitNode(t *testing.T) {
	d := openTestDB(t)

	// Empty DB → no sync → 0
	ts, err := LastSyncForExitNode(d, "karolina")
	if err != nil {
		t.Fatalf("LastSyncForExitNode empty: %v", err)
	}
	if ts != 0 {
		t.Errorf("empty ts = %d, want 0", ts)
	}

	// Add a sync log line mentioning karolina
	if err := AppendExitRuleLog(d, 1, ExitRuleActionSync, "sync karolina: ok"); err != nil {
		t.Fatalf("append: %v", err)
	}
	// And one for a different node
	if err := AppendExitRuleLog(d, 1, ExitRuleActionSync, "sync emilia: ok"); err != nil {
		t.Fatalf("append: %v", err)
	}

	ts, err = LastSyncForExitNode(d, "karolina")
	if err != nil {
		t.Fatalf("LastSyncForExitNode: %v", err)
	}
	if ts == 0 {
		t.Errorf("ts = 0, want > 0 (we just inserted a sync log)")
	}

	// Different node has its own timestamp
	ts2, err := LastSyncForExitNode(d, "emilia")
	if err != nil {
		t.Fatalf("emilia: %v", err)
	}
	if ts2 == 0 {
		t.Errorf("emilia ts = 0, want > 0")
	}

	// Non-existent node → 0
	ts3, err := LastSyncForExitNode(d, "sharlotta")
	if err != nil {
		t.Fatalf("sharlotta: %v", err)
	}
	if ts3 != 0 {
		t.Errorf("sharlotta ts = %d, want 0", ts3)
	}
}

func TestExitRuleLogTime(t *testing.T) {
	// Zero is the "no sync yet" sentinel — format must be empty
	// (not "1970-01-01 03:00:00") so callers can render "никогда".
	if got := ExitRuleLogTime(0); got != "" {
		t.Errorf("ExitRuleLogTime(0) = %q, want empty", got)
	}
	// Non-zero renders
	if got := ExitRuleLogTime(1700000000); got == "" {
		t.Errorf("ExitRuleLogTime(1700000000) = empty, want non-empty")
	}
	// Negative treated as zero (defensive)
	if got := ExitRuleLogTime(-1); got != "" {
		t.Errorf("ExitRuleLogTime(-1) = %q, want empty", got)
	}
}

func TestNextACLVersion(t *testing.T) {
	d := openTestDB(t)

	// Empty table → next is 1
	v, err := NextACLVersion(d)
	if err != nil {
		t.Fatalf("NextACLVersion empty: %v", err)
	}
	if v != 1 {
		t.Errorf("empty next = %d, want 1", v)
	}

	// Save v1 and check next is 2
	if err := SaveACLSnapshot(d, 1, "{}", "skyadmin"); err != nil {
		t.Fatalf("SaveACLSnapshot: %v", err)
	}
	v, err = NextACLVersion(d)
	if err != nil {
		t.Fatalf("NextACLVersion after save: %v", err)
	}
	if v != 2 {
		t.Errorf("after v1 next = %d, want 2", v)
	}

	// Saving v5 manually → next is 6
	if err := SaveACLSnapshot(d, 5, "{}", "skyadmin"); err != nil {
		t.Fatalf("SaveACLSnapshot v5: %v", err)
	}
	v, err = NextACLVersion(d)
	if err != nil {
		t.Fatalf("NextACLVersion after v5: %v", err)
	}
	if v != 6 {
		t.Errorf("after v5 next = %d, want 6", v)
	}
}

func TestMarkACLAppliedAndFail(t *testing.T) {
	d := openTestDB(t)

	if err := SaveACLSnapshot(d, 1, `{"acls":[]}`, "skyadmin"); err != nil {
		t.Fatalf("SaveACLSnapshot: %v", err)
	}

	// Mark applied → applied_success=1
	if err := MarkACLApplied(d, 1); err != nil {
		t.Fatalf("MarkACLApplied: %v", err)
	}
	cfg, err := GetACLConfig(d, 1)
	if err != nil {
		t.Fatalf("GetACLConfig: %v", err)
	}
	if cfg != `{"acls":[]}` {
		t.Errorf("GetACLConfig = %q, want %q", cfg, `{"acls":[]}`)
	}
	if v, _ := LastAppliedACLVersion(d); v != 1 {
		t.Errorf("LastAppliedACLVersion = %d, want 1", v)
	}

	// Save v2, mark fail → applied_success=0, error_msg set
	if err := SaveACLSnapshot(d, 2, `{"acls":[]}`, "skyadmin"); err != nil {
		t.Fatalf("SaveACLSnapshot v2: %v", err)
	}
	if err := MarkACLFail(d, 2, "headscale rejected"); err != nil {
		t.Fatalf("MarkACLFail: %v", err)
	}
	snaps, err := RecentACLSnapshots(d)
	if err != nil {
		t.Fatalf("RecentACLSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 snaps, got %d", len(snaps))
	}
	// Newest first
	if !snaps[0].AppliedSuccess.Valid || snaps[0].AppliedSuccess.Int64 != 0 {
		t.Errorf("snaps[0].AppliedSuccess = %v, want NULL or 0", snaps[0].AppliedSuccess)
	}
	if snaps[0].ErrorMsg != "headscale rejected" {
		t.Errorf("snaps[0].ErrorMsg = %q, want %q", snaps[0].ErrorMsg, "headscale rejected")
	}
	if !snaps[1].AppliedSuccess.Valid || snaps[1].AppliedSuccess.Int64 != 1 {
		t.Errorf("snaps[1].AppliedSuccess = %v, want 1", snaps[1].AppliedSuccess)
	}

	// Last applied is still v1 (v2 failed)
	if v, _ := LastAppliedACLVersion(d); v != 1 {
		t.Errorf("LastAppliedACLVersion after fail = %d, want 1 (v2 failed)", v)
	}
}

func TestGetACLConfigMissing(t *testing.T) {
	d := openTestDB(t)
	// Rollback to a non-existent version must return sql.ErrNoRows
	// so the handler can render 404.
	_, err := GetACLConfig(d, 9999)
	if err == nil {
		t.Fatalf("GetACLConfig(9999) = nil, want error")
	}
}
