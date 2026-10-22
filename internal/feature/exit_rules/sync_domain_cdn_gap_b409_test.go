// sync_domain_cdn_gap_b409_test.go — 2026-10-22 (B409).
//
// The measured defect: the resolver treated a CDN marker as the whole answer and
// skipped DNS entirely (bare `continue`), so a domain whose answers moved outside its
// frozen range set had NO coverage while the database looked fresh:
//
//	agent.minimax.io       → 62.115.253.59   covered by nothing
//	cdn-registry-1.docker.io → 54.157.162.28   covered by nothing
//
// These tests pin the containment rule the fix depends on, because a mistake here
// would either route nothing or hide a genuine gap — and both look the same on the page.
package exit_rules

import "testing"

func TestB409_AnswerInsideARangeIsCovered(t *testing.T) {
	got := b409Uncovered(map[string]bool{"104.18.43.178": true}, []string{"104.16.0.0/12"})
	if len(got) != 0 {
		t.Fatalf("an answer inside the published range must be covered, got %v", got)
	}
}

func TestB409_AnswerOutsideEveryRangeIsAGap(t *testing.T) {
	got := b409Uncovered(
		map[string]bool{"62.115.253.59": true, "54.157.162.28": true, "23.35.104.56": true},
		[]string{"104.16.0.0/12", "23.32.0.0/11"},
	)
	if len(got) != 2 {
		t.Fatalf("want exactly the two uncovered answers, got %v", got)
	}
	for _, want := range []string{"54.157.162.28", "62.115.253.59"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from the gap list %v", want, got)
		}
	}
}

func TestB409_NoStoredRangesMeansEveryAnswerIsAGap(t *testing.T) {
	got := b409Uncovered(map[string]bool{"203.0.113.7": true}, nil)
	if len(got) != 1 {
		t.Fatalf("with no ranges every answer is uncovered, got %v", got)
	}
}

func TestB409_IPv6AndGarbageAreIgnored(t *testing.T) {
	got := b409Uncovered(
		map[string]bool{"2001:db8::1": true, "not-an-ip": true, "198.51.100.7": true},
		[]string{"198.51.100.0/24"},
	)
	if len(got) != 0 {
		t.Fatalf("IPv6 and unparsable values must not become gap rows, got %v", got)
	}
}

func TestB409_GapListIsStableSorted(t *testing.T) {
	a := b409Uncovered(map[string]bool{"62.115.253.59": true, "54.157.162.28": true}, nil)
	b := b409Uncovered(map[string]bool{"54.157.162.28": true, "62.115.253.59": true}, nil)
	if len(a) != 2 || len(b) != 2 || a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("gap order must be stable across map iteration: %v vs %v", a, b)
	}
	if a[0] != "54.157.162.28" {
		t.Fatalf("gap list is not sorted: %v", a)
	}
}
