package admin

import (
	"testing"

	"skygate/internal/db"
)

// TestPickDerpRelayForRun — the probe must prefer the relay the move is about, and fall back to a
// real one instead of the bundled placeholder.
func TestPickDerpRelayForRun(t *testing.T) {
	relays := []db.DerpRelay{
		{ID: 1, Hostname: "derp-old.example.com", URL: "https://derp-old.example.com:8443", Enabled: true},
		{ID: 2, Hostname: "derp-new.example.com", URL: "https://derp-new.example.com:8443", Enabled: true},
		{ID: 3, Hostname: "192.0.2.99", URL: "https://192.0.2.99:8443", Enabled: false},
	}
	run := &db.HostMigration{TargetHost: "198.51.100.10"}
	// No relay mentions the target: the first enabled non-bundled row wins.
	if got, ok := pickDerpRelayForRun(relays, run); !ok || got.ID != 1 {
		t.Fatalf("fallback must pick the first enabled relay, got %+v ok=%v", got, ok)
	}
	// A relay that mentions the target wins over the order.
	relays[1].URL = "https://198.51.100.10:8443"
	if got, ok := pickDerpRelayForRun(relays, run); !ok || got.ID != 2 {
		t.Fatalf("the relay naming the target must win, got %+v ok=%v", got, ok)
	}
	// A nil run must not panic and must still answer.
	if got, ok := pickDerpRelayForRun(relays, nil); !ok || got.ID != 1 {
		t.Fatalf("a nil run must fall back to the first enabled relay, got %+v ok=%v", got, ok)
	}
	// Nothing enabled: explicitly not found, so the caller can name the reason.
	if _, ok := pickDerpRelayForRun([]db.DerpRelay{{ID: 9, Enabled: false}}, run); ok {
		t.Fatal("a disabled-only set must report not found")
	}
}

// TestPickDerpRelayForRun_PrefersNonBundled — the bundled row is a placeholder for the hosted DERP
// map, so probing it proves nothing about the operator's own relay.
func TestPickDerpRelayForRun_PrefersNonBundled(t *testing.T) {
	relays := []db.DerpRelay{
		{ID: 1, Hostname: "bundled", Enabled: true, IsBundled: true},
		{ID: 2, Hostname: "own.example.com", URL: "https://own.example.com:8443", Enabled: true},
	}
	if got, ok := pickDerpRelayForRun(relays, &db.HostMigration{}); !ok || got.ID != 2 {
		t.Fatalf("the operator's own relay must win over the bundled one, got %+v ok=%v", got, ok)
	}
	// With only the bundled row, it is still better than nothing.
	if got, ok := pickDerpRelayForRun(relays[:1], &db.HostMigration{}); !ok || got.ID != 1 {
		t.Fatalf("a bundled-only set must still answer, got %+v ok=%v", got, ok)
	}
}

// TestJoinLimited — a failure report must stay readable: the first few items and a count.
func TestJoinLimited(t *testing.T) {
	if got := joinLimited([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("short lists must be listed whole, got %q", got)
	}
	if got := joinLimited([]string{"a", "b", "c", "d", "e"}, 2); got != "a, b and 3 more" {
		t.Fatalf("long lists must be truncated with a count, got %q", got)
	}
}

// TestMigrateInternalActionNames — the wired set is what the page reports as executable; an empty or
// duplicated set would make the run page lie about what this build can do.
func TestMigrateInternalActionNames(t *testing.T) {
	names := migrateInternalActionNames()
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" || seen[n] {
			t.Fatalf("action names must be non-empty and unique: %q", n)
		}
		seen[n] = true
	}
	for _, want := range []string{"internal_nodes_online", "internal_tags_present",
		"internal_policy_equivalent", "internal_routes_converged", "internal_derp_probe",
		"external_derp_probe_101"} {
		if !seen[want] {
			t.Fatalf("the wired action set must contain %q", want)
		}
	}
}
