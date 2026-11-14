// derp_stun_probe_b472_test.go — a LIVE, opt-in STUN probe of a real relay.
//
// Written while bringing a DERP relay up on a second host: the question «do devices see it and does
// it answer as a client expects» cannot be answered by `ss` (a bound UDP socket says nothing about
// whether the server answers), and a hand-rolled probe is easy to get wrong — the first one written
// for this check omitted the FINGERPRINT attribute and the 4-byte attribute padding, and the OLD,
// healthy relay timed out against it, which would have been read as a broken new relay.
//
// So the probe below is the project's OWN client (`buildSTUNBindingRequest` + `probeSTUNShape`), the
// same pair the /admin/derp tile uses. It is opt-in because it needs a live relay:
//
//	SKYGATE_STUN_PROBE_ADDR=198.51.100.10:3478 go test ./internal/feature/admin/ -run TestB472_STUNProbe -v
//
// Without the variable it SKIPs, so the gate stays green on a machine with no relay.
package admin

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestB472_STUNProbeAgainstLiveRelay(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("SKYGATE_STUN_PROBE_ADDR"))
	if addr == "" {
		t.Skip("SKYGATE_STUN_PROBE_ADDR is unset — no live relay to probe")
	}
	pkt, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build the binding request: %v", err)
	}
	res, err := probeSTUNShape(addr, pkt, txID, 6*time.Second)
	if err != nil {
		t.Fatalf("probe %s: %v — the relay is bound but does not ANSWER a real client "+
			"(check UDP reachability to the STUN port, and that the request carries SOFTWARE+FINGERPRINT)", addr, err)
	}
	if strings.TrimSpace(res.ReflexiveAddr) == "" {
		t.Fatalf("probe %s: a binding success with no mapped address (rtt=%s from=%s)", addr, res.RTT, res.ReplyFrom)
	}
	t.Logf("STUN %s -> mapped %s (rtt %s, reply from %s)", addr, res.ReflexiveAddr, res.RTT, res.ReplyFrom)
}
