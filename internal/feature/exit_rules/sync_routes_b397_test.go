package exit_rules

// sync_routes_b397_test.go — B397: EVERY relay the assignment table names must be
// visited with the UNION of what it owns (table ∪ its own rule candidates).
//
// The live defect this pins (2026-10-18, the operator's installation): `prefix_owner`
// handed emilia 64.233.162.91/32 + 64.233.162.136/32 and sharlotta .93/32 + .190/32 by
// AUTO assignment (cyborg's 16 Google rules claim the prefixes but name no relay). The
// sync visited emilia — a rule named her, so she was in the per-rule map — and applied
// her BASES only, because the per-rule loop could only advertise prefixes that appeared
// in that relay's own candidate list. The "relays the table names" loop that should have
// covered her skipped her precisely BECAUSE a rule already named her
// (relaysFromAssignment's `already` guard). Result: four prefixes served by nobody,
// every device pinned to them black-holed, and only the system test noticed.

import (
	"reflect"
	"testing"
)

// b397LiveOwners is the real table shape from the live installation.
func b397LiveOwners() map[string]string {
	return map[string]string{
		"64.233.162.91/32":  "emilia",
		"64.233.162.136/32": "emilia",
		"64.233.162.93/32":  "sharlotta",
		"64.233.162.190/32": "sharlotta",
		"142.250.0.0/15":    "karolina",
		"172.217.0.0/16":    "karolina",
	}
}

// TestB397_RelaysToSyncIsTheUnion is the regression for the skipped relay: emilia is
// BOTH named by a rule and an owner in the table, and she must appear exactly once.
func TestB397_RelaysToSyncIsTheUnion(t *testing.T) {
	owners := b397LiveOwners()
	// Only karolina is named by a rule (skyworker's 184 rules all name her).
	exitRoutes := map[string][]string{"karolina": {"142.250.0.0/15", "172.217.0.0/16"}}
	got := relaysToSync(owners, exitRoutes)
	want := []string{"emilia", "karolina", "sharlotta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relaysToSync = %v, want %v (the table owners emilia/sharlotta must be visited even though a rule names karolina)", got, want)
	}
}

// TestB397_MergedOwnedPrefixesIsTheUnion pins the live arithmetic: emilia's own rule
// candidates are the base routes only, and the table adds her two /32s. Before the fix
// the apply set was "bases only", which is exactly how those prefixes went unserved.
func TestB397_MergedOwnedPrefixesIsTheUnion(t *testing.T) {
	owners := b397LiveOwners()
	basesOnly := []string{"0.0.0.0/0", "::/0"}
	got := mergedOwnedPrefixes("emilia", basesOnly, owners)
	for _, want := range []string{"64.233.162.91/32", "64.233.162.136/32"} {
		if !b397Contains(got, want) {
			t.Errorf("mergedOwnedPrefixes(emilia) = %v, missing the table-owned %s", got, want)
		}
	}
	if !b397Contains(got, "0.0.0.0/0") || !b397Contains(got, "::/0") {
		t.Errorf("mergedOwnedPrefixes(emilia) = %v, lost a base route (the relay would stop being an exit node)", got)
	}
}

// TestB397_MergedOwnedPrefixesKeepsOneAdvertiser makes sure the union does not leak a
// prefix to a relay that does not own it (B274: exactly one advertiser per prefix).
func TestB397_MergedOwnedPrefixesKeepsOneAdvertiser(t *testing.T) {
	owners := b397LiveOwners()
	// A stale per-rule candidate for a prefix emilia does NOT own must be dropped.
	got := mergedOwnedPrefixes("emilia", []string{"142.250.0.0/15"}, owners)
	if b397Contains(got, "142.250.0.0/15") {
		t.Errorf("mergedOwnedPrefixes(emilia) kept karolina's prefix: %v", got)
	}
}

// TestB397_RelaysToSyncIgnoresGarbage keeps empty owners/nodes out of the apply loop —
// an empty string here used to mean the B352-era stray "" relay.
func TestB397_RelaysToSyncIgnoresGarbage(t *testing.T) {
	got := relaysToSync(map[string]string{"p": ""}, map[string][]string{"": {"1.2.3.0/24"}})
	if len(got) != 0 {
		t.Fatalf("relaysToSync(%v) = %v, want none", map[string]string{"p": ""}, got)
	}
}

func b397Contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
