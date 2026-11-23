package admin

// relay_accept_routes_view_b515_test.go — B515.1.
//
// The page must never guess. "true" is an active loop (danger), "false" is correct, and
// everything else — including an empty string or an error message someone stored — is
// UNKNOWN (warn), because a parser that treats "could not read" as "fine" is exactly how the
// loop stayed invisible for a day.

import "testing"

func TestB515_ObservedTrueIsALoop(t *testing.T) {
	if got := classifyRelayAcceptRoutes("true"); got != relayAcceptRoutesLoop {
		t.Errorf("observed=true must be danger (an active loop), got %q", got)
	}
}

func TestB515_ObservedFalseIsCorrect(t *testing.T) {
	if got := classifyRelayAcceptRoutes("false"); got != relayAcceptRoutesOK {
		t.Errorf("observed=false must be ok, got %q", got)
	}
}

func TestB515_AnythingElseIsUnknownNotFine(t *testing.T) {
	for _, v := range []string{"", "  ", "routeall=<nil>", "error: ssh: connect to host", "0", "1", "TRUE"} {
		if got := classifyRelayAcceptRoutes(v); got != relayAcceptRoutesUnknown {
			t.Errorf("observed=%q must be unknown (never 'fine'), got %q", v, got)
		}
	}
}

// TestB515_AnnotateMatchesHostname is the row/state pairing — the part that can be wrong
// without any of the classifier tests noticing: a store key that does not match the row's
// hostname degrades every row to "unknown", which looks exactly like "nobody has synced yet".
func TestB515_AnnotateMatchesHostname(t *testing.T) {
	nodes := []ExitNodeInfo{
		{Hostname: "emilia", AcceptRoutes: 1},    // the store says false, the desired value still says true
		{Hostname: "Karolina", AcceptRoutes: -1}, // case differs from the store key
		{Hostname: "sharlotta"},                  // the loop
		{Hostname: "aisha"},                      // never measured
	}
	observed := map[string]string{
		"emilia":    "false",
		"karolina":  "false",
		"sharlotta": "true",
	}
	loops, unknown := annotateObservedAcceptRoutes(nodes, observed)

	if nodes[0].ObserveAcceptRoutes != "false" || nodes[0].ObserveAcceptRoutesTone != "ok" {
		t.Errorf("emilia: got %q/%q, want false/ok — the DESIRED value must not leak into the observed column",
			nodes[0].ObserveAcceptRoutes, nodes[0].ObserveAcceptRoutesTone)
	}
	if nodes[1].ObserveAcceptRoutesTone != "ok" {
		t.Errorf("karolina: case-insensitive match failed (got tone %q)", nodes[1].ObserveAcceptRoutesTone)
	}
	if nodes[2].ObserveAcceptRoutesTone != "danger" {
		t.Errorf("sharlotta: an observed true must be danger, got %q", nodes[2].ObserveAcceptRoutesTone)
	}
	if nodes[3].ObserveAcceptRoutes != "" || nodes[3].ObserveAcceptRoutesTone != "warn" {
		t.Errorf("aisha: an absent store entry must be warn/empty, got %q/%q",
			nodes[3].ObserveAcceptRoutes, nodes[3].ObserveAcceptRoutesTone)
	}
	if loops != 1 || unknown != 1 {
		t.Errorf("counts = loops:%d unknown:%d, want 1/1", loops, unknown)
	}
}
