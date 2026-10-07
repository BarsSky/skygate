// telegram_probe_b356_1_test.go — B356.1 (2026-10-07).
//
// The /admin/telegram probe must tell the truth about WHICH path carries the bot:
// the direct probe measures the direct path on purpose, so on the live incident
// deployment it fails while every notification is delivered through a relay tunnel.
// These tests pin that pair of facts, the state vocabulary (which is rendered, so its
// numeric values must not move), and the token redaction on the probe's own error path
// (the probe URL embeds the token, and net/http quotes the URL in its error).
package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"skygate/internal/telegram"
)

func TestB356_1_ProbeStateVocabulary(t *testing.T) {
	// The numeric values are rendered (the template/CSS read State.String, and the
	// value itself is part of the cached/audited wire format) — they must not move.
	if ProbeUnreachable != 0 || ProbeOKDirect != 1 || ProbeOKRelay != 2 || ProbeOKRelayTunnel != 3 {
		t.Fatalf("the probe state numbering is part of the rendered format: %d %d %d %d",
			ProbeUnreachable, ProbeOKDirect, ProbeOKRelay, ProbeOKRelayTunnel)
	}
	if got := ProbeOKRelayTunnel.String(); got != "ok_relay_tunnel" {
		t.Fatalf("the honest new state must have its own identifier, got %q", got)
	}
	for state, want := range map[TelegramProbeState]string{
		ProbeUnreachable:   "unreachable",
		ProbeOKDirect:      "ok_direct",
		ProbeOKRelay:       "ok_relay",
		ProbeOKRelayTunnel: "ok_relay_tunnel",
	} {
		if got := state.String(); got != want {
			t.Errorf("state %d rendered %q, want %q", int(state), got, want)
		}
	}
	if !ProbeOKRelayTunnel.Healthy() {
		t.Fatal("a relay tunnel means the Bot API path WORKS — the page must not show it as a failure")
	}
	if ProbeUnreachable.Healthy() {
		t.Fatal("unreachable is not healthy")
	}
}

func TestB356_1_ProbeReconcilesWithTheLiveEgressState(t *testing.T) {
	blocked := TelegramProbeResult{
		State:   ProbeUnreachable,
		Message: `Get "https://api.telegram.org/bot<redacted>/getMe": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
	}
	relayCarries := telegram.EgressSnapshot{
		Known:          true,
		Enabled:        true,
		Relay:          "karolina",
		RelayEndpoint:  "root@100.64.0.2:18022",
		DirectError:    `Get "https://api.telegram.org/bot<redacted>/getUpdates": context deadline exceeded`,
		Candidates:     []string{"karolina", "emilia", "sharlotta"},
		RelayAt:        time.Now(),
		PreferredRelay: "",
	}

	got := reconcileProbeWithEgress(blocked, relayCarries)
	if got.State != ProbeOKRelayTunnel {
		t.Fatalf("the bot is being carried by a relay, so 'unreachable' would be a lie: %+v", got)
	}
	for _, want := range []string{"karolina", "root@100.64.0.2:18022", "direct path failed"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the state must name %q: %q", want, got.Message)
		}
	}
	if strings.Contains(got.Message, "AAFakeTokenForTests") {
		t.Fatalf("the reconciled message leaked a token: %q", got.Message)
	}

	// The direct probe succeeded: the probe's own (fresher) verdict wins, and the relay
	// that carried the LAST call is reported as a note, not as the state.
	direct := TelegramProbeResult{State: ProbeOKDirect, Message: "Reachable via direct internet"}
	got = reconcileProbeWithEgress(direct, relayCarries)
	if got.State != ProbeOKDirect {
		t.Fatalf("a working direct path is the truth about the direct path: %+v", got)
	}
	if !strings.Contains(got.Message, "karolina") {
		t.Fatalf("the note must say the last call went through the relay: %q", got.Message)
	}

	// Armed but not used yet: still unreachable, with the candidates named.
	armed := telegram.EgressSnapshot{Known: true, Enabled: true, Candidates: []string{"karolina", "emilia"}}
	got = reconcileProbeWithEgress(blocked, armed)
	if got.State != ProbeUnreachable {
		t.Fatalf("nothing has been carried yet, so the state must not claim success: %+v", got)
	}
	for _, want := range []string{"karolina", "emilia"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the armed fallback must name its candidates (%q missing): %q", want, got.Message)
		}
	}

	// Switched off by the operator: say so, because that is a configuration fact.
	off := telegram.EgressSnapshot{Known: true, Enabled: false}
	got = reconcileProbeWithEgress(blocked, off)
	if got.State != ProbeUnreachable {
		t.Fatalf("with the fallback off the direct path is all there is: %+v", got)
	}
	if !strings.Contains(got.Message, telegram.EnvRelayFallback+"=0") {
		t.Fatalf("the page must name the switch that is off: %q", got.Message)
	}

	// Nothing configured and nothing usable: name why.
	none := telegram.EgressSnapshot{Known: true, Enabled: true, Skipped: []string{"karolina: no usable ssh private key"}}
	got = reconcileProbeWithEgress(blocked, none)
	if !strings.Contains(got.Message, "no usable ssh private key") {
		t.Fatalf("a fallback that cannot start must explain itself: %q", got.Message)
	}

	// No egress information at all: the probe renders exactly as it did before B356.1.
	got = reconcileProbeWithEgress(blocked, telegram.EgressSnapshot{})
	if got.State != ProbeUnreachable || got.Message != blocked.Message {
		t.Fatalf("an unknown egress state must not change the probe: %+v", got)
	}
}

func TestB356_1_ProbeMessageNeverCarriesTheToken(t *testing.T) {
	const token = "123456789:AAFakeTokenForTests_0123456789"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Port 1 is closed, so the probe fails with a transport error — and net/http quotes
	// the URL (token included) in that error. The page and the probe cache must never
	// see it.
	res := probeTelegramAPIWithBase(ctx, token, "http://127.0.0.1:1")
	if res.State != ProbeUnreachable {
		t.Fatalf("a closed port must be unreachable, got %s (%s)", res.State, res.Message)
	}
	if strings.Contains(res.Message, token) {
		t.Fatalf("the probe message leaked the bot token: %q", res.Message)
	}
	if !strings.Contains(res.Message, "bot<redacted>") {
		t.Fatalf("the probe message must still identify the API call: %q", res.Message)
	}
}
