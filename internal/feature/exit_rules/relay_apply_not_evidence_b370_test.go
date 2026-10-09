// B370 — an attempt that could not reach a relay because the PORTAL had no
// tailnet is not evidence about the relay.
//
// MEASURED LIVE, 2026-10-09 (reference deployment). The operator updated the
// panel; /admin/update recreated the container at 11:18:51 and the entrypoint
// logged `[init] TS_AUTHKEY_FILE not set — Tailscale skipped`; the app's own
// autostart brought tailscaled up at 11:20:01, ~70 s later. The first sync pass
// started at 11:19:47, so karolina's turn (11:19:52–11:20:07) ran against a
// portal with NO tailnet at all: every rung of the ladder timed out, the 300-char
// failure was written to relay_apply_state:karolina, B309/B352 excluded her from
// the healthy set ("its prefixes move to a relay that can be configured"), her
// 196 prefixes were handed to another relay and the page showed the red
// «Ретранслятор не удалось настроить» banner. None of it was a fact about
// karolina: 90 seconds later the same pass configured emilia over the tailnet
// without a problem, and a manual Re-sync of karolina succeeded on the FIRST rung.
//
// The rule these tests pin: when the portal is off the tailnet and every
// candidate needs that tailnet, the outcome is NOT recorded (the previous state
// stands, so nothing is demoted and no prefix moves) and the reason is named.
package exit_rules

import (
	"database/sql"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

// b370DB is the migrated SQLite database the recording tests write to — the
// native install's backend, and the one whose `roles`-style array columns are
// plain TEXT, so nothing here depends on PostgreSQL.
func b370DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b370.db")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return d
}

// TestB370_AllCandidatesNeedThePortalTailnet is the pure predicate.
func TestB370_AllCandidatesNeedThePortalTailnet(t *testing.T) {
	cases := []struct {
		name  string
		cands []RelayEndpoint
		want  bool
	}{
		{
			"the live case: a tailnet address and a jump through a peer relay",
			[]RelayEndpoint{
				{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"},
				{Kind: RelayEndpointJump, Host: "100.64.0.2", Port: "18022", Jump: "root@100.64.0.3:22"},
				{Kind: RelayEndpointJump, Host: "fd7a:115c:a1e0::2", Port: "18022", Jump: "root@100.64.0.4:22"},
			},
			true,
		},
		{
			"a public address needs no tailnet",
			[]RelayEndpoint{
				{Kind: RelayEndpointTailnet, Host: "100.64.0.2"},
				{Kind: RelayEndpointPublic, Host: "203.0.113.7", Port: "18022"},
			},
			false,
		},
		{
			"a bare node name needs DNS, not the tailnet",
			[]RelayEndpoint{{Kind: RelayEndpointName, Host: "karolina"}},
			false,
		},
		{
			"a jump whose HOP is public can carry it without our tailnet",
			[]RelayEndpoint{
				{Kind: RelayEndpointJump, Host: "100.64.0.2", Jump: "root@203.0.113.9:22"},
			},
			false,
		},
		{
			"a jump whose TARGET is public is not a tailnet-only path",
			[]RelayEndpoint{
				{Kind: RelayEndpointJump, Host: "203.0.113.7", Jump: "root@100.64.0.3:22"},
			},
			false,
		},
		{
			"no candidates at all is a configuration problem, and that IS evidence",
			nil,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllCandidatesNeedThePortalTailnet(tc.cands); got != tc.want {
				t.Errorf("AllCandidatesNeedThePortalTailnet(%+v) = %v, want %v", tc.cands, got, tc.want)
			}
		})
	}
}

// TestB370_NotEvidenceDoesNotOverwriteTheRelayState is the behavioural half:
// the previous state must survive, so the demotion path never sees a failure.
func TestB370_NotEvidenceDoesNotOverwriteTheRelayState(t *testing.T) {
	d := b370DB(t)

	// The relay was configured successfully a moment ago — the live `ok` record.
	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=ok via tailnet", Via: "tailnet", Endpoint: "tailnet 100.64.0.2:18022"})
	before := RelayApplyStateOf(d, "karolina")
	if !before.OK {
		t.Fatalf("setup: the relay should read back OK, got %+v", before)
	}

	// Now the portal loses its own tailnet and every candidate is tailnet-only.
	recordRelayApply(d, "karolina", relayApplyOutcome{
		Label:             "ssh=err=tailnet 100.64.0.2:18022 is not answering: dial tcp 100.64.0.2:18022: i/o timeout",
		NotEvidence:       true,
		NotEvidenceReason: "the portal is not on the tailnet (tailscaled is not running: …)",
	})

	after := RelayApplyStateOf(d, "karolina")
	if !after.OK || after.At != before.At {
		t.Fatalf("a not-evidence outcome overwrote the relay state: before=%+v after=%+v — this is what demotes the relay and moves its prefixes", before, after)
	}
	if failed := after.Failed(before.At+1, 0); failed {
		t.Error("the relay now reads as failed after an attempt that was not about it")
	}
}

// TestB370_ARealFailureIsStillRecorded is the guard rail: the fix must not turn
// every failure into "not evidence".
func TestB370_ARealFailureIsStillRecorded(t *testing.T) {
	d := b370DB(t)
	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=err=Permission denied (publickey)"})

	st := RelayApplyStateOf(d, "karolina")
	if st.OK {
		t.Fatal("a real ssh failure must still be recorded as a failure")
	}
	if !strings.Contains(st.Detail, "Permission denied") {
		t.Errorf("the recorded detail lost the cause: %+v", st)
	}
	if !st.Failed(st.At+1, 0) {
		t.Error("a real failure must still mark the relay failed (that is what demotes it)")
	}
	// And a real attempt still writes the transport record. A failed ladder has
	// no endpoint to name, so the stored value is the "unknown|" placeholder the
	// page renders as «via: unknown» — the point is that the KEY is written at
	// all, which is what the page reads.
	if raw := mustSetting(t, d, RelayApplyViaKey("karolina")); raw != "unknown|" {
		t.Errorf("a real attempt must still write the transport record, got %q", raw)
	}
}

func mustSetting(t *testing.T, d *sql.DB, key string) string {
	t.Helper()
	v, err := skygatedb.GetGlobalSetting(d, key, "")
	if err != nil {
		t.Fatalf("read setting %s: %v", key, err)
	}
	return v
}
