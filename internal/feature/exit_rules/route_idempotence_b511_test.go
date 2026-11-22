package exit_rules

// route_idempotence_b511_test.go — B511 (2026-11-22).
//
// The defect these tests pin is an ABSENCE of a question: the approval step never asked
// what the relay already had, so every pass re-approved an unchanged set and headscale
// fanned a fresh netmap out to every device (36 writes in 24h on the reference host). What
// must stay true: a set comparison that ignores order and duplicates (the two code paths
// build the same list differently), a conservative answer when headscale cannot be read
// (a missing answer is not evidence that nothing changed), and a log line that NAMES the
// prefixes instead of reporting a count.

import (
	"strings"
	"testing"
)

func TestB511_RouteSetComparisonIgnoresOrderAndDuplicates(t *testing.T) {
	a := []string{"0.0.0.0/0", "::/0", "104.16.0.0/12"}
	b := []string{"::/0", "104.16.0.0/12", "0.0.0.0/0"}
	if !routeSetEqual(a, b) {
		t.Error("the same route set in another order must compare equal — the two callers build it differently")
	}
	if !routeSetEqual(a, []string{"0.0.0.0/0", "::/0", "104.16.0.0/12", "104.16.0.0/12"}) {
		t.Error("a duplicated entry is one route, not a change")
	}
	if !routeSetEqual(a, []string{" 0.0.0.0/0 ", "::/0", "104.16.0.0/12"}) {
		t.Error("surrounding whitespace is not a change")
	}
	if routeSetEqual(a, []string{"0.0.0.0/0", "::/0"}) {
		t.Error("a MISSING route is a change")
	}
	if routeSetEqual(a, []string{"0.0.0.0/0", "::/0", "104.16.0.0/12", "8.8.8.8/32"}) {
		t.Error("an ADDED route is a change")
	}
}

func TestB511_CoversIsOneDirectional(t *testing.T) {
	have := []string{"0.0.0.0/0", "::/0", "8.8.8.8/32", "1.1.1.1/32"}
	if !routeSetCovers(have, []string{"8.8.8.8/32", "0.0.0.0/0"}) {
		t.Error("covers must accept a subset")
	}
	if routeSetCovers(have, []string{"9.9.9.9/32"}) {
		t.Error("covers must reject a route that is absent")
	}
	if !routeSetCovers(have, nil) {
		t.Error("covers(nil) is vacuously true")
	}
}

func TestB511_ApprovalNeedsANonEmptyTarget(t *testing.T) {
	// An empty target must NOT read as "already approved": the caller still wants its base
	// routes approved, and answering true would silently skip that.
	if routesAlreadyApproved(nil, nil) {
		t.Error("an empty target is not an approved state")
	}
	if !routesAlreadyApproved([]string{"0.0.0.0/0", "::/0"}, []string{"::/0", "0.0.0.0/0"}) {
		t.Error("both families approved must read as approved")
	}
	if routesAlreadyApproved([]string{"0.0.0.0/0"}, []string{"0.0.0.0/0", "::/0"}) {
		t.Error("a half-approved set must NOT read as approved — IPv6 would never get approved")
	}
}

func TestB511_SortedRouteSetIsDeterministic(t *testing.T) {
	got := strings.Join(sortedRouteSet([]string{"::/0", "0.0.0.0/0", " 8.8.8.8/32", "8.8.8.8/32"}), ",")
	if got != "0.0.0.0/0,8.8.8.8/32,::/0" {
		t.Errorf("sortedRouteSet = %q, want a stable, de-duplicated, trimmed list", got)
	}
}

func TestB511_OwnershipSampleNamesThePrefixes(t *testing.T) {
	before := map[string]string{
		"104.16.0.0/12": "karolina",
		"8.8.8.8/32":    "emilia",
	}
	after := map[string]string{
		"104.16.0.0/12": "emilia", // moved
		"8.8.8.8/32":    "emilia", // unchanged
		"1.1.1.1/32":    "system", // new
	}
	got := ownershipChangeSample(before, after, 6)
	if !strings.Contains(got, "104.16.0.0/12→emilia(was karolina)") {
		t.Errorf("the sample must name a MOVED prefix with both owners, got: %s", got)
	}
	if !strings.Contains(got, "1.1.1.1/32→system(new)") {
		t.Errorf("the sample must name a NEW prefix, got: %s", got)
	}
	if strings.Contains(got, "8.8.8.8/32") {
		t.Errorf("an unchanged prefix is not a change and must not be listed, got: %s", got)
	}

	// Bounded: "inserted=48" must not produce a 48-item log line.
	many := map[string]string{}
	for _, p := range []string{"a/32", "b/32", "c/32", "d/32", "e/32"} {
		many[p] = "relay"
	}
	if got := ownershipChangeSample(nil, many, 2); strings.Count(got, "/32") != 2 {
		t.Errorf("the sample must respect its limit, got: %s", got)
	}
	if got := ownershipChangeSample(before, before, 6); got != "" {
		t.Errorf("nothing moved, so nothing must be named, got: %q", got)
	}
}
