// 2026-08-18 (TD-8, v1.4.4) — unit tests for the pure-Go
// parts of the /admin/system_tests History tab.
//
// The DB-bound code (ComputeTestHistory) is exercised at
// runtime by the live /admin/system_tests page on PG. The
// pure helpers (ParseHistoryWindow, PassRate, TotalRuns,
// truncateForHistory) are pinned here so a typo in the
// date math or the truncation logic surfaces as a Go
// test failure rather than a wrong "Last 7 days" label on
// the page.

package admin

import (
	"strings"
	"testing"
	"time"
)

// TestParseHistoryWindow_7D pins the default 7-day
// window. The handler falls back to this when the
// ?window= param is missing or unrecognised.
func TestParseHistoryWindow_7D(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hw := ParseHistoryWindow("", now)
	if hw.Label != "Last 7 days" {
		t.Errorf("7d default: got label %q, want %q", hw.Label, "Last 7 days")
	}
	want := now.AddDate(0, 0, -7)
	if !hw.Since.Equal(want) {
		t.Errorf("7d default: got since=%s, want %s", hw.Since, want)
	}
	if !hw.Until.IsZero() {
		t.Errorf("7d default: until should be zero, got %s", hw.Until)
	}
}

// TestParseHistoryWindow_7DExplicit pins the explicit
// "7d" param (same as default).
func TestParseHistoryWindow_7DExplicit(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hw := ParseHistoryWindow("7d", now)
	if hw.Label != "Last 7 days" {
		t.Errorf("7d explicit: got label %q", hw.Label)
	}
}

// TestParseHistoryWindow_30D pins the 30-day window.
func TestParseHistoryWindow_30D(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hw := ParseHistoryWindow("30d", now)
	if hw.Label != "Last 30 days" {
		t.Errorf("30d: got label %q", hw.Label)
	}
	want := now.AddDate(0, 0, -30)
	if !hw.Since.Equal(want) {
		t.Errorf("30d: got since=%s, want %s", hw.Since, want)
	}
}

// TestParseHistoryWindow_All pins the all-time window.
// Since should be the unix epoch.
func TestParseHistoryWindow_All(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hw := ParseHistoryWindow("all", now)
	if hw.Label != "All time" {
		t.Errorf("all: got label %q", hw.Label)
	}
	if hw.Since.Unix() != 0 {
		t.Errorf("all: since should be unix epoch, got %s", hw.Since)
	}
	if !hw.Until.IsZero() {
		t.Errorf("all: until should be zero, got %s", hw.Until)
	}
}

// TestParseHistoryWindow_UnknownFallsBackTo7D pins the
// typo-resilience. An operator typing ?window=last-week
// shouldn't 500 the page — they should see the default
// 7-day window.
func TestParseHistoryWindow_UnknownFallsBackTo7D(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	hw := ParseHistoryWindow("last-week", now)
	if hw.Label != "Last 7 days" {
		t.Errorf("unknown: should fall back to 7d, got %q", hw.Label)
	}
}

// TestTestHistoryRow_PassRate pins the pass-rate
// computation. The template colour-codes the cell based
// on this value (green ≥ 95%, yellow 50-95%, red < 50%).
func TestTestHistoryRow_PassRate(t *testing.T) {
	tests := []struct {
		name           string
		pass, fail, sk int
		want           int
	}{
		{"all-pass-100pct", 100, 0, 0, 100},
		{"half-pass-50pct", 50, 50, 0, 50},
		{"no-runs-0pct", 0, 0, 0, 0},
		{"mostly-fail-20pct", 1, 4, 0, 20},
		{"with-skips", 9, 1, 0, 90}, // 9/10 = 90
		{"zero-only-skips", 0, 0, 5, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := TestHistoryRow{PassCount: tc.pass, FailCount: tc.fail, SkipCount: tc.sk}
			if got := r.PassRate(); got != tc.want {
				t.Errorf("%s: pass=%d fail=%d skip=%d: got %d%%, want %d%%",
					tc.name, tc.pass, tc.fail, tc.sk, got, tc.want)
			}
		})
	}
}

// TestTestHistoryRow_TotalRuns pins the TotalRuns helper.
func TestTestHistoryRow_TotalRuns(t *testing.T) {
	r := TestHistoryRow{PassCount: 5, FailCount: 2, SkipCount: 3}
	if got := r.TotalRuns(); got != 10 {
		t.Errorf("TotalRuns: got %d, want 10", got)
	}
	// Empty row.
	empty := TestHistoryRow{}
	if got := empty.TotalRuns(); got != 0 {
		t.Errorf("TotalRuns (empty): got %d, want 0", got)
	}
}

// TestTruncateForHistory_UnderLimit pins the no-truncate
// branch. The full output is returned as-is.
func TestTruncateForHistory_UnderLimit(t *testing.T) {
	got := truncateForHistory("hello", 200)
	if got != "hello" {
		t.Errorf("under limit: got %q, want %q", got, "hello")
	}
}

// TestTruncateForHistory_AtLimit pins the boundary case
// (input length == max). The full string is returned
// (no "..." appended since nothing was truncated).
func TestTruncateForHistory_AtLimit(t *testing.T) {
	in := strings.Repeat("a", 200)
	got := truncateForHistory(in, 200)
	if got != in {
		t.Errorf("at limit: got len=%d, want len=%d", len(got), len(in))
	}
	if strings.Contains(got, "...") {
		t.Errorf("at limit: should NOT append '...', got %q", got)
	}
}

// TestTruncateForHistory_OverLimit pins the truncate
// branch. A 500-char input is clipped to 200 chars + the
// "..." marker.
func TestTruncateForHistory_OverLimit(t *testing.T) {
	in := strings.Repeat("a", 500)
	got := truncateForHistory(in, 200)
	if len(got) != 203 { // 200 chars + "..." (3 chars)
		t.Errorf("over limit: got len=%d, want 203", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("over limit: should end with '...', got %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 200)) {
		t.Errorf("over limit: should start with 200 'a's, got %q", got[:20])
	}
}

// TestTruncateForHistory_Empty pins the empty-input
// branch. An empty string is returned as-is.
func TestTruncateForHistory_Empty(t *testing.T) {
	got := truncateForHistory("", 200)
	if got != "" {
		t.Errorf("empty: got %q, want %q", got, "")
	}
}
