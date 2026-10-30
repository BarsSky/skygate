// system_tests_derp_rows_b447_test.go — B447: the loopback pre-check must actually FIRE.
//
// The first cut of b447Reachable declared the per-address hint map and never filled it, so the
// trap-2 case (the container resolving the relay's own public name to 127.0.0.1) silently fell
// through to a plain dial error — the same class of defect as the bug the block fixes: a
// diagnostic that looks present and answers nothing. This test pins the wiring without touching the
// network: 127.0.0.1 is always resolvable to a loopback address, and a public name is not.
package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"skygate/internal/derphealth"
)

func TestB447_LoopbackHintsFireOnlyForLoopbackAddresses(t *testing.T) {
	targets := []derphealth.RelayProbe{
		{URL: "https://127.0.0.1:443", Addr: "127.0.0.1:443"},
		{URL: "https://relay.invalid:443", Addr: "relay.invalid:443"}, // does not resolve → no hint
		{URL: "https://127.0.0.1:8443", Addr: "127.0.0.1:8443", Skipped: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hints := b447LoopbackHints(ctx, targets)

	if _, ok := hints["127.0.0.1:443"]; !ok {
		t.Fatalf("no hint for the loopback address — the pre-check is wired to nothing again: %#v", hints)
	}
	if _, ok := hints["relay.invalid:443"]; ok {
		t.Fatalf("a name that does not resolve to loopback must not be blamed on trap #2: %#v", hints)
	}
	if _, ok := hints["127.0.0.1:8443"]; ok {
		t.Fatalf("a SKIPPED row is never dialled, so it cannot be the loopback failure: %#v", hints)
	}

	hint := hints["127.0.0.1:443"]
	for _, want := range []string{"derp.probe_host", "--force-recreate", "trap #2"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint must name the fix (%q): %s", want, hint)
		}
	}
}
