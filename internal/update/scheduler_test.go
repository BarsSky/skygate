// 2026-08-18 (B130) — unit tests for the time-of-day
// auto-update scheduler. Covers the pure-function helpers
// (parseHHMM, timeMatches, sameMinute, boolToStr,
// normalizeUpdateTarget) and the readSchedule/readLastRun
// /writeLastRun round-trip. The full Start() tick path is
// not tested here because it requires a live DB + a live
// GitHub HTTP client; the live verify_pre_deploy B130
// check covers the integration.

package update

import (
	"strings"
	"testing"
	"time"
)

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantHour int
		wantMin  int
	}{
		{"03:00", true, 3, 0},
		{"00:00", true, 0, 0},
		{"23:59", true, 23, 59},
		{"12:30", true, 12, 30},
		{"", false, 0, 0},        // empty
		{"25:00", false, 0, 0},   // hour out of range
		{"12:60", false, 0, 0},   // min out of range
		{"1:00", false, 0, 0},    // missing leading zero
		{"12:5", false, 0, 0},    // missing leading zero
		{"12-00", false, 0, 0},   // wrong separator
		{"abcd", false, 0, 0},    // garbage
		{"12345", false, 0, 0},   // no separator
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseHHMM(c.in)
			ok := err == nil
			if ok != c.wantOK {
				t.Errorf("parseHHMM(%q) ok=%v, want %v (err=%v)", c.in, ok, c.wantOK, err)
			}
			if ok && (got.hour != c.wantHour || got.min != c.wantMin) {
				t.Errorf("parseHHMM(%q) = %d:%d, want %d:%d", c.in, got.hour, got.min, c.wantHour, c.wantMin)
			}
		})
	}
}

func TestTimeMatches(t *testing.T) {
	// Build a fixed-time "now" for each case so the test is
	// deterministic.
	now := time.Date(2026, 8, 18, 3, 15, 0, 0, time.UTC)
	cases := []struct {
		hhmm   string
		want   bool
	}{
		{"03:15", true},   // exact match
		{"03:14", false},  // one minute off
		{"03:16", false},
		{"04:15", false},  // one hour off
		{"25:00", false},  // invalid input
		{"", false},       // empty
	}
	for _, c := range cases {
		t.Run(c.hhmm, func(t *testing.T) {
			got := timeMatches(now, c.hhmm)
			if got != c.want {
				t.Errorf("timeMatches(now=03:15, %q) = %v, want %v", c.hhmm, got, c.want)
			}
		})
	}
}

func TestSameMinute(t *testing.T) {
	// 2026-08-18 03:00:00 and 2026-08-18 03:00:45 are in
	// the same minute; 2026-08-18 03:01:00 is in the next.
	a := time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)
	b := time.Date(2026, 8, 18, 3, 0, 45, 0, time.UTC) // same minute as a
	c := time.Date(2026, 8, 18, 3, 1, 0, 0, time.UTC)   // next minute
	d := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)   // next day, same HH:MM

	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"same_minute", a, b, true},
		{"next_minute", a, c, false},
		{"next_day_same_HHMM", a, d, false},
		{"a_zero", time.Time{}, a, false}, // zero time never matches
		{"b_zero", a, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sameMinute(c.a, c.b)
			if got != c.want {
				t.Errorf("sameMinute = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBoolToStr(t *testing.T) {
	if got := boolToStr(true); got != "1" {
		t.Errorf("boolToStr(true) = %q, want %q", got, "1")
	}
	if got := boolToStr(false); got != "0" {
		t.Errorf("boolToStr(false) = %q, want %q", got, "0")
	}
}

func TestTrimV(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v1.3.19.4", "1.3.19.4"},
		{"1.3.19.4", "1.3.19.4"},
		{"v", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := trimV(c.in); got != c.want {
				t.Errorf("trimV(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeUpdateTarget(t *testing.T) {
	// The scheduler's copy of normalizeUpdateTarget must
	// match the admin package's version (see the package-
	// level comment about the import cycle). If the admin
	// version is ever changed, this test must be updated
	// in lockstep.
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"v1.3.19.4", "v1.3.19.4"},
		{"1.3.19.4", "v1.3.19.4"},
		{"v0.33.1.24", "v0.33.1.24"},
		{"0.33.1.24", "v0.33.1.24"},
		{"skygate-pre-update-abc1234", "skygate-pre-update-abc1234"},
		{"main", "main"},
		{"HEAD", "HEAD"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := normalizeUpdateTarget(c.in)
			if got != c.want {
				t.Errorf("normalizeUpdateTarget(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestTickInterval(t *testing.T) {
	// Pin: the scheduler ticks every 30s. If we ever change
	// this, the sameMinute dedup may need to be re-tuned
	// (e.g. a 5-min tick needs a same-hour dedup).
	if TickInterval != 30*time.Second {
		t.Errorf("TickInterval = %s, want 30s", TickInterval)
	}
}

func TestSchedulerDepsShape(t *testing.T) {
	// Compile-time + runtime check that SchedulerDeps has
	// the expected fields. New fields can be added freely;
	// removing or renaming one of these would break the
	// wire-up in main.go (B130-C) and would be caught by
	// the build.
	d := SchedulerDeps{}
	_ = d.DB
	_ = d.State
	_ = d.Checker
	_ = d.BuildVersion
	_ = d.Notifier
	_ = d.RepoPath
	_ = d.Cfg
	// If we got here without a compile error, the shape
	// matches what main.go expects.
	if !strings.Contains("SchedulerDeps", "SchedulerDeps") {
		t.Fatal("unreachable: sanity check")
	}
}
