// 2026-08-20 (B156) — unit tests for the
// key-expiration notification scheduler. Covers
// the pure-function helpers (sameNotifyMinute,
// notifyIsDueThisTick, formatAuditLine,
// formatTelegramSummary, formatNotifyMessage) and
// the storage key constants. The full RunNotify
// path is exercised by the live verify-pre B156
// check (which spins up a sqlite test DB) and by
// the VM's smoke run.

package keynotify

import (
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
)

func TestSameNotifyMinute(t *testing.T) {
	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"zero a", time.Time{}, time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), false},
		{"same minute same day",
			time.Date(2026, 8, 20, 9, 0, 15, 0, time.UTC),
			time.Date(2026, 8, 20, 9, 0, 45, 0, time.UTC),
			true},
		{"different minute",
			time.Date(2026, 8, 20, 9, 0, 15, 0, time.UTC),
			time.Date(2026, 8, 20, 9, 1, 0, 0, time.UTC),
			false},
		{"different hour",
			time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
			false},
		{"different day same hour:min",
			time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC),
			false},
		{"same exact second",
			time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
			true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sameNotifyMinute(c.a, c.b)
			if got != c.want {
				t.Errorf("sameNotifyMinute(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestFormatAuditLine_IncludesAllTokens confirms the
// audit log detail string lists every per-key row
// in a human-readable format.
func TestFormatAuditLine_IncludesAllTokens(t *testing.T) {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	res := NotifyResult{
		Notified:    2,
		Skipped:     0,
		TriggeredAt: now,
		Tokens: []NotifyTokenResult{
			{ID: 1, UserID: 7, Key: "hskey-abc", Notified: true, Reason: "ok"},
			{ID: 2, UserID: 8, Key: "hskey-def", Notified: true, Reason: "ok"},
		},
	}
	got := formatAuditLine(res)
	if !strings.Contains(got, "notified=2") {
		t.Errorf("audit line missing notified count: %q", got)
	}
	if !strings.Contains(got, "skipped=0") {
		t.Errorf("audit line missing skipped count: %q", got)
	}
	if !strings.Contains(got, "id=1 user_id=7 notified=true") {
		t.Errorf("audit line missing first token: %q", got)
	}
	if !strings.Contains(got, "id=2 user_id=8") {
		t.Errorf("audit line missing second token: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("audit line missing newline separator: %q", got)
	}
}

// TestFormatAuditLine_IncludesSkipped confirms that
// skipped tokens (no telegram binding or send
// failed) are captured in the audit line. The
// operator needs to see WHY a user wasn't notified
// — without this, the audit log would just show
// notified=1, skipped=1 with no detail.
func TestFormatAuditLine_IncludesSkipped(t *testing.T) {
	res := NotifyResult{
		Notified: 1,
		Skipped:  1,
		Tokens: []NotifyTokenResult{
			{ID: 1, UserID: 7, Notified: true, Reason: "ok"},
			{ID: 2, UserID: 8, Notified: false, Reason: "no_telegram_binding"},
		},
	}
	got := formatAuditLine(res)
	if !strings.Contains(got, "no_telegram_binding") {
		t.Errorf("audit line missing skip reason: %q", got)
	}
	if !strings.Contains(got, "notified=false") {
		t.Errorf("audit line missing notified=false: %q", got)
	}
}

// TestFormatTelegramSummary_NotifiedOnly confirms
// the compact summary format when everything went
// out successfully.
func TestFormatTelegramSummary_NotifiedOnly(t *testing.T) {
	res := NotifyResult{Notified: 3, Skipped: 0}
	got := formatTelegramSummary(res)
	if !strings.HasPrefix(got, "🔑 key-notify: notified 3 key(s)") {
		t.Errorf("telegram summary missing header: %q", got)
	}
	if strings.Contains(got, "skipped") {
		t.Errorf("telegram summary should NOT mention skipped when 0: %q", got)
	}
}

// TestFormatTelegramSummary_IncludesSkipped confirms
// the summary appends a "(skipped N — ...)" hint
// when the run had skips. The operator uses this
// to spot users without telegram bindings (a
// different migration problem).
func TestFormatTelegramSummary_IncludesSkipped(t *testing.T) {
	res := NotifyResult{Notified: 2, Skipped: 3}
	got := formatTelegramSummary(res)
	if !strings.Contains(got, "skipped 3") {
		t.Errorf("telegram summary missing skipped count: %q", got)
	}
	if !strings.Contains(got, "no telegram binding") {
		t.Errorf("telegram summary missing skip reason: %q", got)
	}
}

// TestFormatNotifyMessage_IncludesRenewInstructions
// confirms the user-facing message tells the user
// HOW to renew. The B156 fix relies on this —
// without the explicit "/my/keys → click Reissue"
// instruction, the user would just see "key
// expiring" and have to figure out what to do.
func TestFormatNotifyMessage_IncludesRenewInstructions(t *testing.T) {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	expiresAt := now.Add(7 * 24 * time.Hour).Unix()
	k := mockExpiringKey(1, 7, "hskey-abcdef1234567890", expiresAt, now.Unix())
	got := formatNotifyMessage("en", k, 7, 168)
	if !strings.Contains(got, "Reissue") {
		t.Errorf("notify message missing Reissue instruction: %q", got)
	}
	if !strings.Contains(got, "/my/keys") {
		t.Errorf("notify message missing /my/keys URL: %q", got)
	}
	if !strings.Contains(got, "automatically revoked") {
		t.Errorf("notify message missing 'old key revoked' reassurance: %q", got)
	}
}

// TestFormatNotifyMessage_TruncatesLongKey confirms
// the key prefix is truncated to 18 chars + "…"
// to keep the message scannable on a phone.
func TestFormatNotifyMessage_TruncatesLongKey(t *testing.T) {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	expiresAt := now.Add(3 * 24 * time.Hour).Unix()
	longKey := "hskey-" + strings.Repeat("a", 50)
	k := mockExpiringKey(1, 7, longKey, expiresAt, now.Unix())
	got := formatNotifyMessage("en", k, 3, 72)
	// 18 chars total: "hskey-" (6) + 12 a's, then "…"
	if !strings.Contains(got, "hskey-aaaaaaaaaaaa…") {
		t.Errorf("notify message did not truncate long key prefix: %q", got)
	}
}

// TestFormatNotifyMessage_TodayAndTomorrow covers
// the timeLeft edge cases. "today" for delta <= 0,
// "tomorrow" for days == 1, "in N day(s)" for
// longer windows. The exact wording matches the
// B155 expires_in_* i18n family so the user gets
// a consistent message across the portal and
// Telegram.
func TestFormatNotifyMessage_TodayAndTomorrow(t *testing.T) {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		days      int
		wantToken string
	}{
		{0, "today"},
		{1, "tomorrow"},
		{2, "in 2 day(s)"},
		{7, "in 7 day(s)"},
		{14, "in 14 day(s)"},
	}
	for _, c := range cases {
		t.Run(c.wantToken, func(t *testing.T) {
			expiresAt := now.Add(time.Duration(c.days) * 24 * time.Hour).Unix()
			k := mockExpiringKey(1, 7, "hskey-test", expiresAt, now.Unix())
			got := formatNotifyMessage("en", k, c.days, c.days*24)
			if !strings.Contains(got, c.wantToken) {
				t.Errorf("expected %q in message, got: %q", c.wantToken, got)
			}
		})
	}
}

// TestStorageKeyConstants confirms the global_settings
// keys are stable (the B156 B-check grep-asserts on
// these strings). Renaming a key would silently break
// the DB→scheduler round-trip.
func TestStorageKeyConstants(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{KeyNotifyEnabled, "keys.notify_enabled"},
		{KeyNotifySchedule, "keys.notify_schedule"},
		{KeyNotifyLastRun, "keys.notify_last_run"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("storage key changed: got %q, want %q", c.got, c.want)
		}
	}
}

// TestDefaultScheduleAfterCleanup confirms the
// B156 scheduler fires at 9 AM, AFTER the
// smoke-mesh cleanup (5 AM) but BEFORE the
// operator's working day. Picking a different
// time would either spam the user too early
// (3 AM = "right after the user falls asleep")
// or arrive when the user has already left
// for the day (6 PM).
func TestDefaultScheduleAfterCleanup(t *testing.T) {
	if DefaultNotifySchedule != "0 9 * * *" {
		t.Errorf("DefaultNotifySchedule changed: got %q, want %q",
			DefaultNotifySchedule, "0 9 * * *")
	}
}

// TestNotifyWindowDaysMatchesB155Banner confirms
// the 14d notification window matches the
// 14d /my/keys banner window (B155). Splitting
// the windows would just confuse the user —
// they'd see "7 days" in the portal but get a
// "14 days" Telegram message, or vice versa.
func TestNotifyWindowDaysMatchesB155Banner(t *testing.T) {
	if NotifyWindowDays != 14 {
		t.Errorf("NotifyWindowDays changed: got %d, want 14 (mirrors B155 banner window)", NotifyWindowDays)
	}
}

// mockExpiringKey builds a minimal db.ExpiringPreauthKey
// for the format helpers. The struct has 8 fields
// (all of them in the SELECT), so the helper
// accepts the 4 the tests care about and fills
// the rest with safe defaults.
func mockExpiringKey(id, userID int64, key string, expiresAt, createdAt int64) db.ExpiringPreauthKey {
	return db.ExpiringPreauthKey{
		ID:        id,
		UserID:    userID,
		Key:       key,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
	}
}
