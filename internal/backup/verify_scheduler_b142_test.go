// v1.4.1 B142 — in-app backup-verify scheduler. Unit tests
// for the pure helpers extracted from the scheduler so the
// runtime path can be tested without a DB / subcommand /
// running skygate binary.
//
// Three functions are tested:
//
//   - tailLines(s, n) — extract the last n non-empty lines.
//   - truncateString(s, max) — clip a string with trailing "...".
//   - sameMinute(a, b) — calendar-minute equality (used to
//     deduplicate scheduler ticks so a 30s tick + slow verify
//     doesn't fire twice in the same minute).
//
// The other functions in verify_scheduler.go (readVerifyEnabled,
// readVerifySchedule, readLastVerifyAt, runVerify) require a
// live *sql.DB and / or a running verify script — they are
// pinned by scripts/check_b142.sh's code-level grep + the
// runtime test on the live VM (operator clicks "Verify now"
// and checks the result).

package backup

import (
	"strings"
	"testing"
	"time"
)

func TestTailLines_Empty(t *testing.T) {
	got := tailLines("", 5)
	if got != "" {
		t.Errorf("expected empty string for empty input, got %q", got)
	}
}

func TestTailLines_FewerThanN(t *testing.T) {
	// Fewer lines than n — return all of them, trimmed.
	got := tailLines("alpha\nbeta\ngamma", 10)
	want := "alpha\nbeta\ngamma"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTailLines_MoreThanN(t *testing.T) {
	// More lines than n — return last n lines, trimmed.
	got := tailLines("line1\nline2\nline3\nline4\nline5", 3)
	want := "line3\nline4\nline5"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTailLines_TrimsTrailingWhitespace(t *testing.T) {
	// Common case: the script's output ends with a newline,
	// so the last "line" is empty after split. tailLines
	// trims the final result.
	got := tailLines("first\nsecond\n", 5)
	want := "first\nsecond"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestTruncateString_UnderLimit(t *testing.T) {
	got := truncateString("short string", 100)
	if got != "short string" {
		t.Errorf("expected unchanged string, got %q", got)
	}
}

func TestTruncateString_AtLimit(t *testing.T) {
	// Exactly max — no truncation needed.
	s := strings.Repeat("a", 50)
	got := truncateString(s, 50)
	if got != s {
		t.Errorf("expected unchanged string at limit, got %q", got)
	}
}

func TestTruncateString_OverLimit(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := truncateString(s, 10)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing '...' marker, got %q", got)
	}
	if len(got) != 13 { // 10 + "..."
		t.Errorf("expected 13 chars (10 + '...'), got %d", len(got))
	}
}

func TestTruncateString_Empty(t *testing.T) {
	got := truncateString("", 10)
	if got != "" {
		t.Errorf("expected empty string for empty input, got %q", got)
	}
}

func TestSameMinute_SameMinute(t *testing.T) {
	a := time.Date(2026, 8, 18, 4, 0, 0, 0, time.UTC)
	b := time.Date(2026, 8, 18, 4, 0, 45, 0, time.UTC) // 45s later
	if !sameMinute(a, b) {
		t.Error("expected sameMinute to return true for 04:00:00 and 04:00:45")
	}
}

func TestSameMinute_DifferentMinutes(t *testing.T) {
	a := time.Date(2026, 8, 18, 4, 0, 0, 0, time.UTC)
	b := time.Date(2026, 8, 18, 4, 1, 0, 0, time.UTC) // next minute
	if sameMinute(a, b) {
		t.Error("expected sameMinute to return false for 04:00 and 04:01")
	}
}

func TestSameMinute_DifferentDays(t *testing.T) {
	a := time.Date(2026, 8, 18, 23, 59, 0, 0, time.UTC)
	b := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC) // next day
	if sameMinute(a, b) {
		t.Error("expected sameMinute to return false for 23:59 and 00:00 next day")
	}
}

func TestSameMinute_ZeroValue(t *testing.T) {
	// The zero time.Time represents "never run yet" — sameMinute
	// must return false so the scheduler proceeds (otherwise the
	// first tick after upgrade would see the empty last-verify-at
	// and skip).
	var zero time.Time
	now := time.Date(2026, 8, 18, 4, 0, 0, 0, time.UTC)
	if sameMinute(zero, now) {
		t.Error("expected sameMinute(zero, now) to return false (zero = never run)")
	}
}

func TestSameMinute_SameSecond(t *testing.T) {
	// Edge case: two timestamps in the same minute but the
	// scheduler's 30s tick lands 1ms off. Same minute → sameMinute
	// returns true (dedup prevents the second tick from re-firing).
	a := time.Date(2026, 8, 18, 4, 0, 0, 100, time.UTC)
	b := time.Date(2026, 8, 18, 4, 0, 59, 999, time.UTC)
	if !sameMinute(a, b) {
		t.Error("expected sameMinute to return true for 04:00:00.1 and 04:00:59.999")
	}
}
