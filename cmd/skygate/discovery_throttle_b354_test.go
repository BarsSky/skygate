// cmd/skygate/discovery_throttle_b354_test.go — B354 (2026-10-06).
//
// The per-peer ENSURE stage of the discovery ticker used to log and audit every
// failure on every tick. Measured live: an empty `cluster` table made all three
// peers fail with `cluster_node_cluster_id_fkey` every five minutes — three journal
// lines and three audit rows per tick, 864 of each per day, in the two places the
// operator uses to find real events. The root cause is fixed in
// EnsureDiscoveredNode (it bootstraps the row); this file pins the noise floor for
// the case where an insert is genuinely broken.
//
// B318 removed exactly this noise from the DISCOVER stage; the ensure stage was
// left behind, which is why the same class needed a second look.
package main

import (
	"testing"
)

func TestB354_EnsureFailureIsReportedOncePerHour(t *testing.T) {
	discoveryEnsureErrorCleared()
	const msg = `karolina: insert discovered node: FOREIGN KEY constraint failed; sharlotta: …; emilia: …`

	if !discoveryEnsureErrorIsNew(msg) {
		t.Fatal("the first failure of a kind must be reported immediately")
	}
	if discoveryEnsureErrorIsNew(msg) {
		t.Fatal("the same failure within the hour must NOT be reported again (864 audit rows a day)")
	}
	if discoveryEnsureErrorIsNew(msg) {
		t.Fatal("still the same failure — still suppressed")
	}
	// A DIFFERENT failure is news, however soon it arrives: suppression is keyed on
	// the message, not on "we already complained once".
	if !discoveryEnsureErrorIsNew(msg + " (now with a different cause)") {
		t.Fatal("a different failure must be reported")
	}
	// A successful tick clears the slot, so the next occurrence is immediate.
	discoveryEnsureErrorCleared()
	if !discoveryEnsureErrorIsNew(msg) {
		t.Fatal("after a successful tick the same failure is news again")
	}
	// An empty message is not a failure at all.
	if discoveryEnsureErrorIsNew("") {
		t.Fatal("an empty message must never be reported")
	}
}

// TestB354_TheTwoStagesDoNotShareASlot: a discover failure must not suppress an
// ensure failure (they are different stages with different fixes).
func TestB354_TheTwoStagesDoNotShareASlot(t *testing.T) {
	discoveryErrorCleared()
	discoveryEnsureErrorCleared()
	if !discoveryErrorIsNew(errFake("tailscaled not running")) {
		t.Fatal("discover: first failure reported")
	}
	if discoveryErrorIsNew(errFake("tailscaled not running")) {
		t.Fatal("discover: repeated failure suppressed")
	}
	if !discoveryEnsureErrorIsNew("karolina: foreign key") {
		t.Fatal("the ensure stage has its own slot — a discover failure must not silence it")
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
