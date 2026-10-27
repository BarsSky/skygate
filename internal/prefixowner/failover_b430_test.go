// internal/prefixowner/failover_b430_test.go — B430.
//
// The operator's install can lose a relay at any moment; when that happens the
// engine moves every prefix that relay owned to a healthy one and rewrites the
// per-CIDR `via=` pins. The move is correct — and until B430 it was completely
// silent: the only trace was a log line, while devices stopped reaching those
// destinations. These tests pin which moves are NEWS and which are bookkeeping.
package prefixowner

import "testing"

// TestB430_OnlyAForcedMoveIsNews: a prefix that LEFT a relay which is no longer
// healthy is what the operator must hear about.
func TestB430_OnlyAForcedMoveIsNews(t *testing.T) {
	prev := map[string]string{
		"104.16.0.0/12": "emilia",   // owner went down → news
		"8.8.8.0/24":    "karolina", // owner stayed healthy but moved → rebalance, no news
		"1.1.1.0/24":    "emilia",   // did not move → nothing
	}
	next := map[string]string{
		"104.16.0.0/12": "karolina",
		"8.8.8.0/24":    "emilia",
		"1.1.1.0/24":    "emilia",
	}
	moves := DescribeFailover(prev, next, []string{"karolina", "sharlotta"})
	if len(moves) != 1 {
		t.Fatalf("moves = %+v, want exactly the prefix that left the unhealthy relay", moves)
	}
	m := moves[0]
	if m.Prefix != "104.16.0.0/12" || m.From != "emilia" || m.To != "karolina" || !m.Forced {
		t.Fatalf("move = %+v, want 104.16.0.0/12 emilia→karolina (forced)", m)
	}
}

// TestB430_EmptyHealthySetIsNotAnOutage: headscale unreachable is not "all relays
// are down". The engine falls back to the relays the rules named, so a move in that
// pass must not page anyone.
func TestB430_EmptyHealthySetIsNotAnOutage(t *testing.T) {
	prev := map[string]string{"104.16.0.0/12": "emilia"}
	next := map[string]string{"104.16.0.0/12": "karolina"}
	if moves := DescribeFailover(prev, next, nil); len(moves) != 0 {
		t.Fatalf("an empty healthy set produced %+v — that is a missing measurement, not an outage", moves)
	}
}

// TestB430_FirstAssignmentIsNotAFailover: a prefix with no previous owner (a first
// pass, a rebuilt table, a restore) is new bookkeeping, not a relay dying.
func TestB430_FirstAssignmentIsNotAFailover(t *testing.T) {
	next := map[string]string{"104.16.0.0/12": "karolina"}
	if moves := DescribeFailover(map[string]string{}, next, []string{"karolina"}); len(moves) != 0 {
		t.Fatalf("a first assignment was reported as a failover: %+v", moves)
	}
	if moves := DescribeFailover(nil, next, []string{"karolina"}); len(moves) != 0 {
		t.Fatalf("a nil previous table was reported as a failover: %+v", moves)
	}
}
