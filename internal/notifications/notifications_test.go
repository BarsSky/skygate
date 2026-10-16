// 2026-08-20 (B157.1) — unit tests for the pure-function
// helpers: TimeAgo (humanize relative time) +
// TypeIcon (per-type FontAwesome class) +
// TypeSeverityColor (per-severity CSS class suffix).
//
// 2026-10-16 (B392): TimeAgo is localised now, so these tests run against the
// installed catalogue and pin BOTH languages. TestMain installs the catalogue —
// without it i18n.T returns the raw key and every assertion here would compare
// against "notif.time_just_now".

package notifications

import (
	"os"
	"strings"
	"testing"
	"time"

	"skygate/internal/i18n"
)

func TestMain(m *testing.M) {
	i18n.SetGlobal(i18n.New())
	os.Exit(m.Run())
}

func TestTimeAgo(t *testing.T) {
	// 2026-08-20 12:00:00 UTC is the reference.
	ref := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"zero", time.Time{}, ""},
		{"now", ref, "just now"},
		{"30s ago", ref.Add(-30 * time.Second), "just now"},
		{"44s ago", ref.Add(-44 * time.Second), "just now"},
		{"45s ago", ref.Add(-45 * time.Second), "1 min ago"},
		{"89s ago", ref.Add(-89 * time.Second), "1 min ago"},
		{"90s ago", ref.Add(-90 * time.Second), "1 min ago"},
		{"5 min ago", ref.Add(-5 * time.Minute), "5 min ago"},
		{"59 min ago", ref.Add(-59 * time.Minute), "59 min ago"},
		{"60 min ago", ref.Add(-60 * time.Minute), "1 h ago"},
		{"90 min ago", ref.Add(-90 * time.Minute), "1 h ago"},
		{"2 h ago", ref.Add(-2 * time.Hour), "2 h ago"},
		{"23 h ago", ref.Add(-23 * time.Hour), "23 h ago"},
		{"24 h ago", ref.Add(-24 * time.Hour), "1 d ago"},
		{"2 d ago", ref.Add(-2 * 24 * time.Hour), "2 d ago"},
		{"29 d ago", ref.Add(-29 * 24 * time.Hour), "29 d ago"},
		{"30 d ago", ref.Add(-30 * 24 * time.Hour), "4 wk ago"},
		// 60 days = ~2 months. The weeks
		// threshold caps at 30d; the months
		// threshold kicks in at 30d+ and uses
		// 30-day months.
		{"60 d ago", ref.Add(-60 * 24 * time.Hour), "2 mo ago"},
		{"120 d ago", ref.Add(-120 * 24 * time.Hour), "4 mo ago"},
		{"364 d ago", ref.Add(-364 * 24 * time.Hour), "12 mo ago"},
		{"365 d ago", ref.Add(-365 * 24 * time.Hour), "2025-08-20"},
		{"2 years ago", ref.Add(-2 * 365 * 24 * time.Hour), "2024-08-20"},
		// Future timestamp (clock skew) — the
		// helper should clamp to "just now"
		// rather than render a negative time.
		{"future 5s", ref.Add(5 * time.Second), "just now"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TimeAgo(i18n.LangEN, c.at, ref)
			if got != c.want {
				t.Errorf("TimeAgo(en, %v, %v) = %q, want %q", c.at, ref, got, c.want)
			}
		})
	}
}

// TestTimeAgo_RussianIsRussian — B392: the value used to be hardcoded English,
// so a Russian panel said "2 h ago" next to Russian labels while the RU keys sat
// unused in the catalogue. Every unit must now come from the catalogue, and no
// English word may survive.
func TestTimeAgo_RussianIsRussian(t *testing.T) {
	ref := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"just now", ref.Add(-10 * time.Second), "только что"},
		{"minutes", ref.Add(-5 * time.Minute), "5 мин назад"},
		{"hours", ref.Add(-3 * time.Hour), "3 ч назад"},
		{"days", ref.Add(-3 * 24 * time.Hour), "3 дн назад"},
		{"weeks", ref.Add(-30 * 24 * time.Hour), "4 нед назад"},
		{"months", ref.Add(-60 * 24 * time.Hour), "2 мес назад"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TimeAgo(i18n.LangRU, c.at, ref)
			if got != c.want {
				t.Errorf("TimeAgo(ru, %v) = %q, want %q", c.at, got, c.want)
			}
			for _, en := range []string{"ago", "min", "hour", "day"} {
				if strings.Contains(got, en) {
					t.Errorf("TimeAgo(ru, %v) = %q still contains the English word %q", c.at, got, en)
				}
			}
		})
	}
}

// TestNotificationFilterFor — an unknown filter value must mean "all", never a
// silently empty list (B392).
func TestNotificationFilterFor(t *testing.T) {
	cases := map[string]string{
		"unread":     "unread",
		"all":        "all",
		"":           "all",
		"severity=x": "all",
		"UNREAD":     "all",
	}
	for in, want := range cases {
		if got := string(NotificationFilterFor(in)); got != want {
			t.Errorf("NotificationFilterFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTypeIcon(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{TypeKeyExpiring, "fa-key"},
		{"cert.renewal", "fa-bell"}, // future type — fallback
		{"unknown.type", "fa-bell"}, // unknown — fallback
		{"", "fa-bell"},             // empty — fallback
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			got := TypeIcon(c.typ)
			if got != c.want {
				t.Errorf("TypeIcon(%q) = %q, want %q", c.typ, got, c.want)
			}
		})
	}
}

func TestTypeSeverityColor(t *testing.T) {
	cases := []struct {
		severity string
		want     string
	}{
		{SeverityInfo, "info"},
		{SeverityWarn, "warn"},
		{SeverityDanger, "danger"},
		{"unknown", ""}, // empty string — caller concatenates with "notif-icon-" prefix
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.severity, func(t *testing.T) {
			got := TypeSeverityColor(c.severity)
			if got != c.want {
				t.Errorf("TypeSeverityColor(%q) = %q, want %q", c.severity, got, c.want)
			}
		})
	}
}

// TestTypeIconFallbackForUnknownTypes confirms the
// "add a new notification type without updating
// this switch" contract: a future B-check that adds
// e.g. "cert.renewal" gets a generic fa-bell icon
// (not an empty string or a missing class) so the
// page still renders something reasonable.
func TestTypeIconFallbackForUnknownTypes(t *testing.T) {
	if got := TypeIcon("cert.renewal"); !strings.HasPrefix(got, "fa-") {
		t.Errorf("TypeIcon fallback should still be a fa-* class, got %q", got)
	}
	if got := TypeIcon(""); !strings.HasPrefix(got, "fa-") {
		t.Errorf("TypeIcon empty should still be a fa-* class, got %q", got)
	}
}

// TestSeverityConstantsDocumented confirms the
// severity constants are well-known values
// (B153/B155 use the same strings).
func TestSeverityConstantsDocumented(t *testing.T) {
	// Pin the string values. If a future refactor
	// changes these, the test fails — the B153
	// badge-* CSS class names depend on them.
	if SeverityInfo != "info" || SeverityWarn != "warn" || SeverityDanger != "danger" {
		t.Errorf("severity constants changed: info=%q warn=%q danger=%q",
			SeverityInfo, SeverityWarn, SeverityDanger)
	}
}
