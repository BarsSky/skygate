// route_truth_b441_test.go — B441 (2026-10-29) — «инструмент что проверяет конечный адрес
// и собирает необходимую информацию о том какие подсети и адреса необходимо также
// добавить в список на маршрутизацию».
//
// The defect these tests pin is one sentence with two halves: the ASSIGNMENT TABLE decides
// who serves a prefix (B275), but the ADVERTISEMENT was still built from the rules. So a
// prefix the table had handed to a relay that no rule names — an operator pin, an auto
// assignment of a relay-less rule, an approved B440 relay request — was announced by
// nobody, while the ACL pinned clients to that relay. Measured on the staggered path,
// which is the one a default install runs.
package exit_rules

import (
	"sort"
	"strings"
	"testing"
)

// TestB441_TableOwnedPrefixIsAdvertisedWithoutARule is the live shape of the bug: the
// table says karolina serves 104.16.0.0/12, no rule of karolina names it, and the rule
// that DOES name it lives on emilia.
func TestB441_TableOwnedPrefixIsAdvertisedWithoutARule(t *testing.T) {
	owners := map[string]string{"104.16.0.0/12": "karolina", "8.8.8.0/24": "emilia"}

	got := mergedOwnedPrefixes("karolina", nil, owners)
	if len(got) != 1 || got[0] != "104.16.0.0/12" {
		t.Fatalf("karolina must advertise the table-owned prefix with no rule naming it, got %v", got)
	}

	// ...and the relay whose RULE claims it must NOT keep advertising it: one prefix, one
	// advertising relay, or headscale's primary flaps (B274).
	emilia := mergedOwnedPrefixes("emilia", []string{"104.16.0.0/12"}, owners)
	for _, p := range emilia {
		if p == "104.16.0.0/12" {
			t.Fatalf("emilia still advertises a prefix the table gave karolina: %v", emilia)
		}
	}
}

// TestB441_RelayWithOnlyTablePrefixesIsVisited: the staggered pass iterates the relays it
// knows about, and the table may name one that no rule does — exactly the relay an
// approved request just pinned. It must be visited, or its advertisement is never written.
func TestB441_RelayWithOnlyTablePrefixesIsVisited(t *testing.T) {
	owners := map[string]string{"104.16.0.0/12": "karolina"}
	exitRoutes := map[string][]string{"emilia": {"104.16.0.0/12"}}
	got := relaysToSync(owners, exitRoutes)
	sort.Strings(got)
	want := []string{"emilia", "karolina"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("relays to sync = %v, want %v — the table-owned relay must be visited", got, want)
	}
}

// TestB441_FirstPassStillUsesTheRuleCandidate: before the first reconcile the table is
// empty, and the rules ARE the only evidence. The union must not drop them, or a fresh
// install would advertise nothing at all.
func TestB441_FirstPassStillUsesTheRuleCandidate(t *testing.T) {
	got := mergedOwnedPrefixes("emilia", []string{"198.51.100.0/24"}, nil)
	if len(got) != 1 || got[0] != "198.51.100.0/24" {
		t.Fatalf("an unwritten table row must keep the rule's candidate, got %v", got)
	}
}

// TestB441_BaseRoutesAreNeverDroppedByTheUnion: the union is fed to
// `tailscale set --advertise-routes=`, which REPLACES the whole set. The base exit routes
// are prepended elsewhere, and this test documents that the union itself filters nothing
// beyond ownership.
func TestB441_BaseRoutesSurviveTheUnion(t *testing.T) {
	owners := map[string]string{"0.0.0.0/0": "karolina", "::/0": "karolina"}
	got := mergedOwnedPrefixes("karolina", nil, owners)
	if len(got) != 2 {
		t.Fatalf("base routes must survive the union, got %v", got)
	}
}

// TestB441_DedupBetweenTableAndRules: the same prefix can arrive from both halves (the
// table and the relay's own rules) and must be applied ONCE — a duplicate in
// `--advertise-routes` is rejected by tailscale and would fail the whole apply.
func TestB441_DedupBetweenTableAndRules(t *testing.T) {
	owners := map[string]string{"203.0.113.0/24": "emilia"}
	got := mergedOwnedPrefixes("emilia", []string{"203.0.113.0/24", "203.0.113.0/24"}, owners)
	if len(got) != 1 {
		t.Fatalf("the union must de-duplicate, got %v", got)
	}
}
