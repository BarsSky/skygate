// internal/localtime/localtime_b429_test.go — B429.
//
// The operator's report was about a DISPLAY and a SCHEDULE disagreeing with the
// clock he reads. These tests pin the three rules the fix rests on: an explicit
// zone is authoritative, a typo is an error rather than a silent UTC, and every
// rendered timestamp carries the zone (so a screenshot cannot be misread again).
package localtime

import (
	"strings"
	"testing"
	"time"
)

func TestB429_ExplicitZoneWinsAndCarriesItsName(t *testing.T) {
	defer ResetForTest()

	resolved, err := Set("Europe/Moscow")
	if err != nil {
		t.Fatalf("Set(Europe/Moscow): %v", err)
	}
	if resolved != "Europe/Moscow" {
		t.Fatalf("resolved = %q, want Europe/Moscow", resolved)
	}
	if got := Name(); got != "Europe/Moscow" {
		t.Fatalf("Name() = %q", got)
	}
	// 05:12 UTC is 08:12 in Moscow — the operator's release time.
	utc := time.Date(2026, 10, 27, 5, 12, 0, 0, time.UTC)
	if got := Format(utc); got != "2026-10-27 08:12 MSK" {
		t.Fatalf("Format(05:12Z) in Moscow = %q, want 2026-10-27 08:12 MSK", got)
	}
	if got := FormatClock(utc); got != "08:12" {
		t.Fatalf("FormatClock = %q, want 08:12", got)
	}
	if got := FormatDate(utc); got != "2026-10-27" {
		t.Fatalf("FormatDate = %q", got)
	}
	// The instant itself is unchanged — only its rendering.
	if Now().Location().String() != "Europe/Moscow" {
		t.Fatalf("Now() is not in the configured zone: %s", Now().Location())
	}
}

// TestB429_UnknownZoneIsAnErrorNotASilentUTC is the rule that keeps a typo from
// moving every timestamp in the panel.
func TestB429_UnknownZoneIsAnErrorNotASilentUTC(t *testing.T) {
	defer ResetForTest()
	if _, err := Set("Europe/Nowhere"); err == nil {
		t.Fatal("an unknown zone was accepted — a typo would silently move every timestamp")
	}
	if got := Name(); got == "Europe/Nowhere" {
		t.Fatalf("a failed Set changed the zone to %q", got)
	}
	if _, err := Set("Europe/Nowhere"); err == nil || !strings.Contains(err.Error(), "Europe/Nowhere") {
		t.Fatalf("the error must name the value that was refused, got %v", err)
	}
}

// TestB429_AutoFollowsTheSystem: "auto" (and an empty value) resolve to what the
// host reports, and the resolution says which name it used.
func TestB429_AutoFollowsTheSystem(t *testing.T) {
	defer ResetForTest()
	for _, value := range []string{"", "auto", "AUTO"} {
		resolved, err := Set(value)
		if err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
		if want := DetectSystem(); resolved != want {
			t.Fatalf("Set(%q) resolved to %q, want the detected %q", value, resolved, want)
		}
	}
}

// TestB429_DSTIsTheZonesProblemNotOurs: a zone with daylight saving must render
// the operator's wall clock, which is what an offset constant cannot do.
func TestB429_DSTIsTheZonesProblemNotOurs(t *testing.T) {
	defer ResetForTest()
	if _, err := Set("Europe/Berlin"); err != nil {
		t.Fatalf("Set(Europe/Berlin): %v", err)
	}
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if got := In(summer).Format("15:04"); got != "14:00" {
		t.Fatalf("summer 12:00Z rendered as %q, want 14:00 (CEST)", got)
	}
	if got := In(winter).Format("15:04"); got != "13:00" {
		t.Fatalf("winter 12:00Z rendered as %q, want 13:00 (CET)", got)
	}
}

// TestB429_UnixAndZero: the template helper's historical contract — a
// non-positive unix value renders as a dash — is preserved, in the new zone.
func TestB429_UnixAndZero(t *testing.T) {
	defer ResetForTest()
	if _, err := Set("Europe/Moscow"); err != nil {
		t.Fatal(err)
	}
	if got := FormatUnix(0); got != "—" {
		t.Fatalf("FormatUnix(0) = %q, want —", got)
	}
	if got := FormatUnix(1793088000); !strings.HasSuffix(got, "MSK") {
		t.Fatalf("FormatUnix must carry the zone: %q", got)
	}
	if got := Format(time.Time{}); !strings.Contains(got, "0001-01-01") {
		t.Fatalf("a zero time must stay recognisable, got %q", got)
	}
}
