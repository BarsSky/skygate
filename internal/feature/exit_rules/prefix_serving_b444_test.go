// prefix_serving_b444_test.go — B444. The rule is pure: two maps in, one report out.
package exit_rules

import (
	"strings"
	"testing"
)

// TestB444_ServedWhenTheOwnerAdvertisesIt is the healthy case — nothing to report.
func TestB444_ServedWhenTheOwnerAdvertisesIt(t *testing.T) {
	owners := map[string]string{"203.0.113.0/24": "emilia"}
	rep := ReportServing(owners, map[string][]string{"emilia": {"203.0.113.0/24", "0.0.0.0/0"}})
	if rep.Owned != 1 || rep.Served != 1 || len(rep.Unserved) != 0 {
		t.Fatalf("report = %+v, want 1 owned / 1 served / 0 unserved", rep)
	}
}

// TestB444_CoveringRangeCounts: the CDN path advertises the provider's RANGE, so a /32 member
// of an advertised /12 IS served. Missing this would make the diagnostic cry wolf for every
// Cloudflare destination.
func TestB444_CoveringRangeCounts(t *testing.T) {
	owners := map[string]string{
		"104.16.0.5/32":  "karolina",
		"104.16.0.0/12":  "karolina",
		"142.250.1.1/32": "karolina",
	}
	rep := ReportServing(owners, map[string][]string{"karolina": {"104.16.0.0/12", "142.250.0.0/15"}})
	if rep.Served != 3 || len(rep.Unserved) != 0 {
		t.Fatalf("report = %+v — a prefix inside an advertised range must count as served", rep)
	}
}

// TestB444_OwnerThatDoesNotAdvertiseIsReported is the live black hole: the table gives the
// prefix to a relay the relay itself never advertises.
func TestB444_OwnerThatDoesNotAdvertiseIsReported(t *testing.T) {
	owners := map[string]string{"198.51.100.0/24": "emilia"}
	rep := ReportServing(owners, map[string][]string{"emilia": {"0.0.0.0/0"}})
	if rep.Served != 0 || len(rep.Unserved) != 1 {
		t.Fatalf("report = %+v, want exactly one unserved prefix", rep)
	}
	if rep.Unserved[0].Owner != "emilia" || rep.Unserved[0].Prefix != "198.51.100.0/24" {
		t.Errorf("unserved entry = %+v, want the prefix and its owner named", rep.Unserved[0])
	}
	if rep.Unserved[0].Reason == "" {
		t.Error("an unserved prefix without a reason is not actionable")
	}
}

// TestB444_RelayMissingFromHeadscaleIsNamed: a relay in the table that headscale does not
// know (deleted, renamed, never registered) is a DIFFERENT failure than "does not advertise",
// and the operator has to be told which one it is.
func TestB444_RelayMissingFromHeadscaleIsNamed(t *testing.T) {
	owners := map[string]string{"8.8.8.0/24": "ghost"}
	rep := ReportServing(owners, map[string][]string{"emilia": {"0.0.0.0/0"}})
	if len(rep.Unserved) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(rep.Unserved[0].Reason, "not in headscale") {
		t.Errorf("reason = %q, want it to name the missing relay", rep.Unserved[0].Reason)
	}
}

// TestB444_UnownedPrefixIsReported: a table row with an empty owner serves nobody, and must
// be visible rather than silently skipped.
func TestB444_UnownedPrefixIsReported(t *testing.T) {
	rep := ReportServing(map[string]string{"1.1.1.0/24": ""}, map[string][]string{"emilia": {"0.0.0.0/0"}})
	if len(rep.Unserved) != 1 || !strings.Contains(rep.Unserved[0].Reason, "no owner") {
		t.Fatalf("report = %+v", rep)
	}
}

// TestB444_EmptyTableIsQuiet: a fresh install has no assignments yet and must not report a
// black hole for the whole internet.
func TestB444_EmptyTableIsQuiet(t *testing.T) {
	rep := ReportServing(nil, nil)
	if rep.Owned != 0 || len(rep.Unserved) != 0 || rep.Served != 0 {
		t.Fatalf("report = %+v, want an empty report", rep)
	}
}

// TestB444_NotAppliedMetric pins the periodic half: it reads the SAME label the flash shows,
// so the metric cannot disagree with the operator's screen.
func TestB444_NotAppliedMetric(t *testing.T) {
	owners := map[string]string{
		"104.16.0.0/12":  "emilia",    // applied
		"142.250.0.0/15": "karolina",  // SSH failed
		"8.8.8.0/24":     "sharlotta", // never visited
	}
	result := map[string]string{
		"emilia":   "ssh=ok approved=3",
		"karolina": "ssh=err=connection refused",
	}
	if got := CountOwnedPrefixesNotApplied(owners, result); got != 2 {
		t.Fatalf("not-applied = %d, want 2 (the failed relay and the unvisited one)", got)
	}
	if got := CountOwnedPrefixesNotApplied(map[string]string{"104.16.0.0/12": "emilia"}, result); got != 0 {
		t.Fatalf("not-applied = %d, want 0 for a healthy relay", got)
	}
	if got := CountOwnedPrefixesNotApplied(map[string]string{"p": ""}, result); got != 1 {
		t.Fatalf("an unowned prefix must count as not applied, got %d", got)
	}
}

// TestB444_RelayApplyLooksFailed pins the label reading itself, including the "info=" case
// (a relay deliberately skipped is NOT an apply of its prefixes).
func TestB444_RelayApplyLooksFailed(t *testing.T) {
	cases := map[string]bool{
		"ssh=ok approved=3":              false,
		"local=ok via helper approved=2": false,
		"ssh=err=dial tcp: refused":      true,
		"local=err=helper said no":       true,
		"info=no rules target this node": true,
		"":                               true,
	}
	for label, want := range cases {
		if got := relayApplyLooksFailed(label); got != want {
			t.Errorf("relayApplyLooksFailed(%q) = %v, want %v", label, got, want)
		}
	}
}

// TestB444_MetricsArePublished pins the periodic half end to end: the gauges exist, carry the
// names the dashboard scrapes, and a sync pass's numbers reach them. Without this a metric can
// silently stay at 0 forever — the exact failure mode `skygate_db_size_bytes` had (collector.go
// records it), where a swallowed error left a healthy install reporting zero.
func TestB444_MetricsArePublished(t *testing.T) {
	if got := prefixesOwnedGauge.Name(); got != "skygate_exit_rules_prefixes_owned" {
		t.Fatalf("owned gauge name = %q", got)
	}
	if got := prefixesNotAppliedGauge.Name(); got != "skygate_exit_rules_prefixes_not_applied" {
		t.Fatalf("not-applied gauge name = %q", got)
	}

	owners := map[string]string{"104.16.0.0/12": "emilia", "8.8.8.0/24": "karolina"}
	result := map[string]string{"emilia": "ssh=ok approved=2", "karolina": "ssh=err=timeout"}
	publishPrefixServingMetrics(owners, result)
	if got := prefixesOwnedGauge.Value(); got != 2 {
		t.Errorf("owned gauge = %v, want 2", got)
	}
	if got := prefixesNotAppliedGauge.Value(); got != 1 {
		t.Errorf("not-applied gauge = %v, want 1 (karolina's apply failed)", got)
	}

	// A clean pass moves the not-applied gauge back to zero — a gauge that only ever grows
	// would keep a stale failure on the dashboard forever.
	publishPrefixServingMetrics(owners, map[string]string{"emilia": "ssh=ok", "karolina": "ssh=ok approved=1"})
	if got := prefixesNotAppliedGauge.Value(); got != 0 {
		t.Errorf("not-applied gauge = %v after a clean pass, want 0", got)
	}
}
