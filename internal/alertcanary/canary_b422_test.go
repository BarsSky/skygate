// internal/alertcanary/canary_b422_test.go — B422.
//
// The canary exists because a channel that never speaks looks exactly like a broken one.
// Its policy is therefore table-tested as a PURE function, and its one side effect (send
// then persist) is driven with a fake sender against a real SQLite database.
package alertcanary

import (
	"database/sql"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

func b422DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/canary.db")
	if err != nil {
		t.Skipf("sqlite unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	telegram.ResetAlertDeliveryForTest()
	t.Cleanup(telegram.ResetAlertDeliveryForTest)
	return d
}

type fakeSender struct {
	events []telegram.Alert
	nextID int64
}

func (f *fakeSender) SendAlertEvent(a telegram.Alert) int64 {
	f.events = append(f.events, a)
	f.nextID++
	return f.nextID
}

// TestB422_CanaryPolicyIsPureAndBoring pins every branch of the decision. The cases are
// the ones that matter operationally: a restart is not a canary, a week of silence IS one,
// and no recipient means no attempt (the ledger already says why).
func TestB422_CanaryPolicyIsPureAndBoring(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	first := 10 * time.Minute

	cases := []struct {
		name      string
		boot      time.Time
		last      time.Time
		interval  time.Duration
		targetOK  bool
		wantSend  bool
		wantWhyIn string
	}{
		{"no recipient", now.Add(-time.Hour), time.Time{}, week, false, false, "no alert recipient"},
		{"fresh install waits firstDelay", now, time.Time{}, week, true, false, "firstDelay"},
		{"fresh install after firstDelay sends", now.Add(-11 * time.Minute), time.Time{}, week, true, true, "first canary"},
		{"within the interval stays quiet", now.Add(-48 * time.Hour), now.Add(-time.Hour), week, true, false, "within"},
		{"interval elapsed sends", now.Add(-48 * time.Hour), now.Add(-8 * 24 * time.Hour), week, true, true, "interval elapsed"},
		{"disabled by interval 0", now.Add(-48 * time.Hour), time.Time{}, 0, true, false, "disabled"},
		{"exactly at the interval sends", now.Add(-time.Hour), now.Add(-week), week, true, true, "interval elapsed"},
	}
	for _, c := range cases {
		got := nextCanary(now, c.boot, c.last, c.interval, first, c.targetOK)
		if got.Send != c.wantSend {
			t.Errorf("%s: Send=%v want %v (reason %q)", c.name, got.Send, c.wantSend, got.Reason)
			continue
		}
		if c.wantWhyIn != "" && !contains(got.Reason, c.wantWhyIn) {
			t.Errorf("%s: reason %q does not mention %q", c.name, got.Reason, c.wantWhyIn)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestB422_CanarySendsAndPersists drives the real tickAt: it must send ONE alert through
// the alert entry point and persist the timestamp so a restart does not repeat it.
func TestB422_CanarySendsAndPersists(t *testing.T) {
	d := b422DB(t)
	// A configured recipient: token + chat_id.
	for k, v := range map[string]string{"telegram.bot_token": "123:abc", "telegram.chat_id": "4242"} {
		if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ($1, $2)`, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	sender := &fakeSender{}
	now := time.Now()
	c := New(Config{DB: d, Sender: sender, Interval: 7 * 24 * time.Hour, FirstDelay: time.Minute, Now: func() time.Time { return now }})

	c.tickAt(now, now.Add(-time.Hour)) // boot an hour ago → the first canary is due
	if len(sender.events) != 1 {
		t.Fatalf("sent %d canary event(s), want 1", len(sender.events))
	}
	ev := sender.events[0]
	if ev.TitleKey != "telegram.alert_canary_title" {
		t.Fatalf("title key = %q", ev.TitleKey)
	}
	if ev.DedupKey != "" {
		t.Fatalf("the canary must never be deduped, got DedupKey=%q", ev.DedupKey)
	}
	if len(ev.Facts) < 3 {
		t.Fatalf("facts = %+v, want delivered/failed/recipient", ev.Facts)
	}

	// Persisted → a second tick a minute later sends nothing.
	now = now.Add(time.Minute)
	c.tickAt(now, now.Add(-time.Hour))
	if len(sender.events) != 1 {
		t.Fatalf("a second canary was sent inside the interval (events=%d)", len(sender.events))
	}

	// The interval elapsing does send again.
	now = now.Add(8 * 24 * time.Hour)
	c.tickAt(now, now.Add(-time.Hour))
	if len(sender.events) != 2 {
		t.Fatalf("the canary did not fire after the interval (events=%d)", len(sender.events))
	}
}

// TestB422_CanaryDoesNotPersistAFailedSend: a failed canary must be RETRIED, not counted as
// sent — otherwise one network blip silences the signal for the whole interval.
func TestB422_CanaryDoesNotPersistAFailedSend(t *testing.T) {
	d := b422DB(t)
	sender := &fakeSender{}
	now := time.Now()
	c := New(Config{DB: d, Sender: sender, Interval: time.Hour, FirstDelay: time.Minute, Now: func() time.Time { return now }})

	// No token configured → resolveAlertTarget says not configured → no attempt at all.
	c.tickAt(now, now.Add(-time.Hour))
	if len(sender.events) != 0 {
		t.Fatalf("the canary attempted a send with no recipient (events=%d)", len(sender.events))
	}
	if got := loadLastSent(d); !got.IsZero() {
		t.Fatalf("last-sent was persisted for a non-send (%v)", got)
	}

	// A recipient appears; the very next tick must try (nothing was marked sent).
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ('telegram.bot_token','123:abc')`); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ('telegram.chat_id','4242')`); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	c.tickAt(now, now.Add(-time.Hour))
	if len(sender.events) != 1 {
		t.Fatalf("the canary did not fire once a recipient existed (events=%d)", len(sender.events))
	}
	if got := loadLastSent(d); got.IsZero() {
		t.Fatal("last-sent was not persisted after a delivered canary")
	}
}
