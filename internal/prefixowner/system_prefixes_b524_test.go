package prefixowner

// system_prefixes_b524_test.go — B524 (2026-11-27). «Публичный резолвер не должен ехать
// маршрутом, и строка, которая его везла, должна умереть».
//
// The measured defect: 1.1.1.1/32 and 8.8.8.8/32 were carried as system prefixes, so a
// relay advertised them. A client that accepts subnet routes then sent every DNS query to
// that relay — a US host for a Moscow operator — because a /32 is more specific than the
// exit node's 0.0.0.0/0. The exit node itself was healthy; the operator saw "exit-node не
// работает, какой бы ни выбрал".
//
// Removing the prefixes from the carried set is only half the fix: the rows must be
// RELEASED, otherwise the relays keep advertising them forever. That release happens in
// EnsureSystemPrefixes, and it needs the nil-vs-empty distinction this block introduced.

import (
	"testing"
)

// TestB524_EmptySetReleasesSystemRows — the core of the release half: "nothing should be
// carried" must delete every system row, and it must NOT touch a row a human or a rule owns.
func TestB524_EmptySetReleasesSystemRows(t *testing.T) {
	src := newB458DB(t)
	if _, _, err := EnsureSystemPrefixes(src.DB, []string{"1.1.1.1/32", "8.8.8.8/32"}, []string{"emilia"}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A prefix an operator pinned by hand must survive the release: pruning is about OUR
	// rows, never about somebody else's decision (B275's manual pin rule).
	if _, err := src.DB.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		VALUES ('203.0.113.7/32', 'karolina', 'manual', 0, 0, 1)`); err != nil {
		t.Fatalf("seed manual row: %v", err)
	}

	added, removed, err := EnsureSystemPrefixes(src.DB, []string{}, []string{"emilia"}, nil)
	if err != nil {
		t.Fatalf("EnsureSystemPrefixes(empty): %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v, want nothing added for an empty set", added)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want both public resolver rows released", removed)
	}
	var n int
	if err := src.DB.QueryRow(`SELECT COUNT(*) FROM prefix_owner WHERE source = 'system'`).Scan(&n); err != nil {
		t.Fatalf("count system rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("system rows left = %d, want 0 — a released resolver must stop being advertised", n)
	}
	if owner, source := ownerOf(t, src, "203.0.113.7/32"); owner != "karolina" || source != "manual" {
		t.Fatalf("manual row was disturbed by the release: owner=%q source=%q", owner, source)
	}
}

// TestB524_NilSetIsANoOp — a caller that does not manage the set (nil) must not release
// anything: the release above is powerful enough to take DNS away from a tailnet, so it is
// reachable only by a caller that explicitly asked to manage it.
func TestB524_NilSetIsANoOp(t *testing.T) {
	src := newB458DB(t)
	if _, _, err := EnsureSystemPrefixes(src.DB, []string{"192.168.13.53/32"}, []string{"emilia"}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	added, removed, err := EnsureSystemPrefixes(src.DB, nil, []string{"emilia"}, nil)
	if err != nil {
		t.Fatalf("EnsureSystemPrefixes(nil): %v", err)
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("nil set did work: added=%v removed=%v", added, removed)
	}
	if owner, source := ownerOf(t, src, "192.168.13.53/32"); owner == "" || source != SystemSource {
		t.Fatalf("nil set released a row: owner=%q source=%q", owner, source)
	}
}
