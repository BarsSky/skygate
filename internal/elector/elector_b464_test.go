// B464 (2026-11-11) — `degraded` must obey the staleness rule.
//
// The elector owns ONE question: "has this node stopped beating?" A `degraded` node
// is alive by definition (its agent just authenticated), so it must never be
// flipped by the liveness pass while it beats — but the moment it goes SILENT it
// must become `failed` like any other node, or a host that dies while degraded stays
// `degraded` forever: the panel would keep saying "alive, but not serving" about a
// box that is switched off.
package elector

import (
	"database/sql"
	"testing"
	"time"
)

func TestB464_NextState_Degraded(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * time.Second)

	fresh := sql.NullTime{Time: now.Add(-10 * time.Second), Valid: true}
	stale := sql.NullTime{Time: now.Add(-17 * time.Minute), Valid: true}

	if got, _ := nextState("degraded", fresh, sql.NullTime{}, cutoff); got != "degraded" {
		t.Fatalf("fresh degraded → %q, want degraded (a heartbeat is proof of life)", got)
	}
	if got, reason := nextState("degraded", stale, sql.NullTime{}, cutoff); got != "failed" || reason == "" {
		t.Fatalf("stale degraded → (%q, %q), want failed with a reason", got, reason)
	}
	// A degraded row with no last_seen at all cannot claim to be alive either: the
	// pre-B464 machine had no such row, but a partial restore can produce one.
	if got, _ := nextState("degraded", sql.NullTime{}, sql.NullTime{}, cutoff); got != "degraded" {
		t.Fatalf("degraded with no last_seen → %q, want it left alone (nothing proves it is silent)", got)
	}
	// The other states must be untouched by this block.
	if got, _ := nextState("ready", stale, sql.NullTime{}, cutoff); got != "failed" {
		t.Fatalf("ready stale → %q, want failed (B204 unchanged)", got)
	}
	if got, _ := nextState("draining", stale, sql.NullTime{}, cutoff); got != "draining" {
		t.Fatalf("draining → %q, want draining (the operator's decision)", got)
	}
}
