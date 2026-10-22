package exit_rules

// domain_rows_verdict_b411_test.go — the decision a diagnostic may make about a
// domain rule's derived rows, separated from the DNS race that made the old
// assertion unsatisfiable (see domain_rows_verdict_b411.go).

import (
	"testing"
	"time"
)

const b411Hour = int64(3600)

func TestB411_CoveredWinsRegardlessOfTiming(t *testing.T) {
	cases := []struct {
		name        string
		uncovered   int
		lastResolve int64
	}{
		{"covered, just resolved", 0, 1000},
		{"covered, resolved long ago", 0, 1000 - 100*b411Hour},
		{"covered, never resolved", 0, 0},
	}
	for _, c := range cases {
		if got := DomainRowsVerdictFor(c.uncovered, c.lastResolve, 1000, 6*time.Hour); got != DomainRowsCovered {
			t.Errorf("%s: got %v, want covered", c.name, got)
		}
	}
}

func TestB411_RotationAfterTheLastResolveIsPendingNotStale(t *testing.T) {
	// The live case: resolved 20 minutes ago (interval 6h), and the provider
	// answered with a new edge address since.
	got := DomainRowsVerdictFor(3, 1000, 1000+20*60, 6*time.Hour)
	if got != DomainRowsPendingNextPass {
		t.Fatalf("an answer newer than the last resolve must be pending-next-pass, got %v", got)
	}
	note := DomainRowsVerdictNote(got, 3, 1000, 1000+20*60, 6*time.Hour)
	if note == "" {
		t.Fatal("the pending verdict must explain itself on the panel")
	}
}

func TestB411_ElapsedIntervalWithUncoveredAnswersIsStale(t *testing.T) {
	// The pre-B409 defect: the marker said "handled", the interval elapsed, and
	// the rows still do not cover the answers.
	got := DomainRowsVerdictFor(2, 1000, 1000+7*b411Hour, 6*time.Hour)
	if got != DomainRowsStale {
		t.Fatalf("an elapsed interval plus uncovered answers must be stale, got %v", got)
	}
}

func TestB411_NeverResolvedIsStale(t *testing.T) {
	got := DomainRowsVerdictFor(1, 0, 5_000_000, 6*time.Hour)
	if got != DomainRowsStale {
		t.Fatalf("a rule that was never resolved must be stale, got %v", got)
	}
	if note := DomainRowsVerdictNote(got, 1, 0, 5_000_000, 6*time.Hour); note == "" {
		t.Fatal("the never-resolved verdict must say so")
	}
}

func TestB411_DisabledGateIsPending(t *testing.T) {
	// interval 0 = resolve every tick (the pre-B308 behaviour), so a difference
	// is transient by construction — never a failure.
	if got := DomainRowsVerdictFor(4, 1, 999_999, 0); got != DomainRowsPendingNextPass {
		t.Fatalf("a disabled gate must be pending-next-pass, got %v", got)
	}
}

func TestB411_BackwardsClockIsNotTheRowsFault(t *testing.T) {
	// The stamp is in the future (clock went backwards). B308 resolves such a
	// domain again; blaming the rows for the clock would be a false FAIL.
	if got := DomainRowsVerdictFor(2, 2000, 1000, 6*time.Hour); got != DomainRowsPendingNextPass {
		t.Fatalf("a future stamp must be pending-next-pass, got %v", got)
	}
}

func TestB411_BoundariesAreExactlyInclusive(t *testing.T) {
	interval := 6 * time.Hour
	// One second short of the interval: still pending.
	if got := DomainRowsVerdictFor(1, 1000, 1000+int64(interval/time.Second)-1, interval); got != DomainRowsPendingNextPass {
		t.Fatalf("one second before the interval must be pending, got %v", got)
	}
	// Exactly at the interval: due — and stale if still uncovered.
	if got := DomainRowsVerdictFor(1, 1000, 1000+int64(interval/time.Second), interval); got != DomainRowsStale {
		t.Fatalf("exactly at the interval must be stale, got %v", got)
	}
}

func TestB411_Humanize(t *testing.T) {
	cases := map[int64]string{
		0:         "0s",
		45:        "45s",
		600:       "10m",
		3600:      "1h",
		3900:      "1h5m",
		6 * 3600:  "6h",
		7 * 86400: "168h",
		-5:        "0s",
	}
	for in, want := range cases {
		if got := humanizeSeconds(in); got != want {
			t.Errorf("humanizeSeconds(%d) = %q, want %q", in, got, want)
		}
	}
}
