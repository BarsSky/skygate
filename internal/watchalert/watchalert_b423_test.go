// internal/watchalert/watchalert_b423_test.go — B423.
//
// Both policies are pure functions and both side effects are driven through an injected
// clock and a fake sender, so "an onset alerts, a chronic condition does not spam, and a
// recovery is announced" is a table test rather than a wait.
package watchalert

import (
	"strings"
	"testing"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

type fakeSender struct{ events []telegram.Alert }

func (f *fakeSender) SendAlertEvent(a telegram.Alert) int64 {
	f.events = append(f.events, a)
	return int64(len(f.events))
}

func setup(t *testing.T, now *time.Time) *fakeSender {
	t.Helper()
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	telegram.ResetAlertDeliveryForTest()
	s := &fakeSender{}
	Configure(Config{Sender: s, Now: func() time.Time { return *now }, DiscoveryInterval: 6 * time.Hour,
		LoginWindow: 10 * time.Minute, LoginThreshold: 10, LoginCooldown: time.Hour})
	t.Cleanup(ResetForTest)
	return s
}

// TestB423_DiscoveryOnsetThenEscalationThenRecovery is the whole discovery policy.
func TestB423_DiscoveryOnsetThenEscalationThenRecovery(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	s := setup(t, &now)

	DiscoveryFailure("tailscale status --json: exit 1: failed to connect")
	if len(s.events) != 1 {
		t.Fatalf("the ONSET did not alert (events=%d)", len(s.events))
	}
	if s.events[0].Severity != telegram.SeverityWarn {
		t.Fatalf("onset severity = %v, want warn", s.events[0].Severity)
	}
	if s.events[0].DedupKey != "watchalert.discovery" {
		t.Fatalf("dedup key = %q", s.events[0].DedupKey)
	}

	// Still failing one hour later: silence (the journal keeps the hourly record).
	now = now.Add(time.Hour)
	DiscoveryFailure("same")
	if len(s.events) != 1 {
		t.Fatalf("a chronic failure alerted again inside the escalation window (events=%d)", len(s.events))
	}

	// Six hours after the onset: alert again — it is not going away.
	now = now.Add(6 * time.Hour)
	DiscoveryFailure("same")
	if len(s.events) != 2 {
		t.Fatalf("the escalation window elapsed without an alert (events=%d)", len(s.events))
	}

	// Recovery is announced exactly once.
	DiscoverySuccess()
	if len(s.events) != 3 {
		t.Fatalf("recovery was not announced (events=%d)", len(s.events))
	}
	if s.events[2].Severity != telegram.SeverityRecovery {
		t.Fatalf("recovery severity = %v", s.events[2].Severity)
	}
	DiscoverySuccess()
	if len(s.events) != 3 {
		t.Fatalf("a second successful tick re-announced the recovery (events=%d)", len(s.events))
	}

	// A NEW failure after a success is news again, immediately.
	now = now.Add(time.Minute)
	DiscoveryFailure("broken again")
	if len(s.events) != 4 {
		t.Fatalf("the second onset did not alert (events=%d)", len(s.events))
	}
}

// TestB423_DiscoveryPolicyTable pins the pure decision, including the boundary.
func TestB423_DiscoveryPolicyTable(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		last    time.Time
		failing bool
		every   time.Duration
		want    bool
	}{
		{"first failure", time.Time{}, false, 6 * time.Hour, true},
		{"chronic inside window", now.Add(-time.Hour), true, 6 * time.Hour, false},
		{"chronic at the boundary", now.Add(-6 * time.Hour), true, 6 * time.Hour, true},
		{"never alerted but failing", time.Time{}, true, 6 * time.Hour, true},
		{"escalation disabled", now.Add(-100 * time.Hour), true, 0, false},
	}
	for _, c := range cases {
		got, why := discoveryAlertDue(now, c.last, c.failing, c.every)
		if got != c.want {
			t.Errorf("%s: due=%v want %v (%s)", c.name, got, c.want, why)
		}
	}
}

// TestB423_LoginBurstAlertsOncePerCooldown: ten failures in the window alert; the next nine
// do not; a fresh burst after the cooldown does.
func TestB423_LoginBurstAlertsOncePerCooldown(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	s := setup(t, &now)

	for i := 0; i < 9; i++ {
		now = now.Add(time.Second)
		LoginFailure("203.0.113.9:4000")
	}
	if len(s.events) != 0 {
		t.Fatalf("a burst below the threshold alerted (events=%d)", len(s.events))
	}
	now = now.Add(time.Second)
	LoginFailure("203.0.113.9:4001")
	if len(s.events) != 1 {
		t.Fatalf("the threshold crossing did not alert (events=%d)", len(s.events))
	}
	ev := s.events[0]
	if ev.TitleKey != "alert.login_burst.title" {
		t.Fatalf("title = %q", ev.TitleKey)
	}
	if len(ev.Facts) < 3 {
		t.Fatalf("facts = %+v, want count/window/ips", ev.Facts)
	}
	// The same source must appear ONCE (the port is not a source).
	var ipsFact string
	for _, f := range ev.Facts {
		if strings.Contains(f.Value, "203.0.113.9") {
			ipsFact = f.Value
		}
	}
	if strings.Count(ipsFact, "203.0.113.9") != 1 {
		t.Fatalf("the same IP was counted as several sources: %q", ipsFact)
	}
	if strings.Contains(ipsFact, "4001") {
		t.Fatalf("the alert leaks the source PORT: %q", ipsFact)
	}

	// Another ten failures immediately: the cooldown keeps it quiet.
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		LoginFailure("198.51.100.7:5000")
	}
	if len(s.events) != 1 {
		t.Fatalf("a second burst alerted inside the cooldown (events=%d)", len(s.events))
	}

	// After the cooldown a new burst alerts again, and its counter starts fresh.
	now = now.Add(time.Hour)
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		LoginFailure("198.51.100.7:5001")
	}
	if len(s.events) != 2 {
		t.Fatalf("a burst after the cooldown did not alert (events=%d)", len(s.events))
	}
}

// TestB423_LoginPolicyTable pins the pure decision.
func TestB423_LoginPolicyTable(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		count     int
		threshold int
		last      time.Time
		cooldown  time.Duration
		want      bool
	}{
		{"below threshold", 9, 10, time.Time{}, time.Hour, false},
		{"at threshold, never alerted", 10, 10, time.Time{}, time.Hour, true},
		{"at threshold inside cooldown", 10, 10, now.Add(-time.Minute), time.Hour, false},
		{"at threshold past cooldown", 10, 10, now.Add(-2 * time.Hour), time.Hour, true},
		{"threshold disabled", 100, 0, time.Time{}, time.Hour, false},
	}
	for _, c := range cases {
		if got := loginBurstDue(c.count, c.threshold, now, c.last, c.cooldown); got != c.want {
			t.Errorf("%s: due=%v want %v", c.name, got, c.want)
		}
	}
}

// TestB423_TheWindowSlides: old failures must leave the window, or a slow trickle would
// eventually cross the threshold and look like a burst.
func TestB423_TheWindowSlides(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	s := setup(t, &now)
	for i := 0; i < 9; i++ {
		now = now.Add(time.Second)
		LoginFailure("203.0.113.9:1")
	}
	// Eleven minutes later (outside the 10-minute window) the old hits are gone.
	now = now.Add(11 * time.Minute)
	for i := 0; i < 9; i++ {
		now = now.Add(time.Second)
		LoginFailure("203.0.113.9:1")
	}
	if len(s.events) != 0 {
		t.Fatalf("expired hits were still counted (events=%d)", len(s.events))
	}
}
