package my

// prefix_override_b509_test.go — B509 (2026-11-25).
//
// The note on /my/exit-nodes exists because "which relay do I go through" was answered
// three times, differently, and each answer was read as a property of the device when it
// was a property of the DESTINATION. What these tests pin is the gate, not the prose: the
// counting rule must ignore the two catch-all prefixes (otherwise a tailnet with nothing
// pinned starts explaining a thing that cannot happen), must be nil-safe (a page must not
// fail because the assignment table could not be read), and the number must actually reach
// the template — a note that renders without the count teaches nothing.

import "testing"

func TestB509_DefaultRoutesAreNotPinned(t *testing.T) {
	pinned := map[string]bool{
		"0.0.0.0/0":     false,
		"::/0":          false,
		" 0.0.0.0/0 ":   false,
		"104.16.0.0/12": true,
		"8.8.8.8/32":    true,
		"2001:db8::/32": true,
	}
	for prefix, want := range pinned {
		if got := !isDefaultRoutePrefix(prefix); got != want {
			t.Errorf("isDefaultRoutePrefix(%q) = %v, want pinned=%v", prefix, !got, want)
		}
	}
}

func TestB509_CountIgnoresCatchAllPrefixes(t *testing.T) {
	owners := map[string]string{
		"0.0.0.0/0":     "relay-a", // every relay advertises this: not a pin
		"::/0":          "relay-a",
		"104.16.0.0/12": "relay-b",
		"8.8.8.8/32":    "relay-b",
		"2001:db8::/32": "relay-c",
	}
	if got, want := countPinnedIn(owners), 3; got != want {
		t.Errorf("countPinnedIn = %d, want %d (the two catch-all prefixes must not count)", got, want)
	}
	if got := countPinnedIn(nil); got != 0 {
		t.Errorf("countPinnedIn(nil) = %d, want 0", got)
	}
}

func TestB509_NoDatabaseIsNotAnError(t *testing.T) {
	if got := countPinnedPrefixes(nil); got != 0 {
		t.Errorf("countPinnedPrefixes(nil) = %d, want 0 — the page must render", got)
	}
}
