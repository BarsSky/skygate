// B464 (2026-11-11) — "ready" must mean "this host can serve traffic".
//
// THE MEASUREMENT this file pins down (operator's standby, live):
//
//	cluster_node: svyatoslava | state=ready | roles={skygate-standby} | last_seen 24 s
//	on that host: no web server (nothing on 80/443/8080/8082), no headscale,
//	              no database, /var/lib/skygate = 16 KB
//
// The state was set by the only signal that existed — "a process on that host reached
// the hub's /api/cluster/heartbeat" — which says nothing about serving. The tests
// below are the two halves of the fix: the VERDICT (pure, no DB) and the STATE
// MACHINE that applies it (real SQLite, real migrations).
package cluster

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
)

// ---------- the verdict (pure) ------------------------------------------------

// TestB464_Verdict covers the four cases the hub has to distinguish. The zero case
// (nil payload) is the compatibility guarantee: an old agent must never be demoted
// for a field it cannot send.
func TestB464_Verdict(t *testing.T) {
	cases := []struct {
		name       string
		in         *Readiness
		wantState  string
		wantSubstr string // "" means "do not check the sentence"
	}{
		{
			name:      "no payload (old agent) keeps the pre-B464 rule",
			in:        nil,
			wantState: "",
		},
		{
			name:       "nothing listening",
			in:         &Readiness{Serving: false, HealthURL: "http://127.0.0.1:8080/healthz", ProbeError: "connection refused"},
			wantState:  NodeStateDegraded,
			wantSubstr: "no skygate web server answered",
		},
		{
			name:       "no answer and no error text",
			in:         &Readiness{Serving: false, HealthURL: "http://127.0.0.1:8080/healthz"},
			wantState:  NodeStateDegraded,
			wantSubstr: "no answer",
		},
		{
			name: "serving but the instance itself says not ready",
			in: &Readiness{
				Serving: true, Ready: false,
				HealthURL: "http://127.0.0.1:8080/healthz",
				DB:        "error: dial tcp 127.0.0.1:5432: connect: connection refused",
				Headscale: "skipped",
			},
			wantState:  NodeStateDegraded,
			wantSubstr: "NOT READY",
		},
		{
			name: "serving and ready",
			in: &Readiness{
				Serving: true, Ready: true,
				HealthURL: "http://127.0.0.1:8080/healthz",
				Build:     "v1.5.210+dd2f623b",
				DB:        "ok",
			},
			wantState:  NodeStateReady,
			wantSubstr: "database reachable",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, reason := readinessVerdict(c.in)
			if state != c.wantState {
				t.Fatalf("state = %q, want %q", state, c.wantState)
			}
			if c.wantSubstr != "" && !strings.Contains(reason, c.wantSubstr) {
				t.Fatalf("reason = %q, want it to contain %q", reason, c.wantSubstr)
			}
			if c.in != nil && reason == "" {
				t.Fatal("reason is empty — a degraded row with no sentence is the same lie as ready-with-nothing-serving")
			}
			if c.in != nil && strings.Contains(reason, `"`) {
				t.Fatalf("reason carries a bare quote, which extractAuditReason would cut at: %q", reason)
			}
		})
	}
}

// TestB464_SanitizeAuditReason — the reason now carries text produced by a REMOTE
// host (/readyz's driver message), and two readers cannot survive a quote or a
// newline in it: the %q-built audit detail (extractAuditReason scans for the next
// bare `"`) and the HTML cell.
func TestB464_SanitizeAuditReason(t *testing.T) {
	got := sanitizeAuditReason("db said \"no\"\nand\tstopped\x00 here")
	if strings.ContainsAny(got, "\"\n\t\x00") {
		t.Fatalf("control/quote bytes survived: %q", got)
	}
	if !strings.Contains(got, "db said 'no'") {
		t.Fatalf("the sentence lost its meaning: %q", got)
	}
	// Rune-boundary truncation: a 3-byte character at the cut must not become invalid
	// UTF-8 (which %q would render as a broken escape into JSONB).
	long := strings.Repeat("ж", 500)
	cut := sanitizeAuditReason(long)
	if !strings.HasSuffix(cut, "…") {
		t.Fatalf("long reason was not truncated: %d runes", len([]rune(cut)))
	}
	if strings.ContainsRune(cut, '\uFFFD') {
		t.Fatal("truncation split a multi-byte character")
	}
}

// TestB464_AuditJSON — an absent payload must read as `null`, not as an all-zero
// object: "the old agent did not measure" and "measured, everything false" are
// different facts and a reader (and B464's own audit review) must tell them apart.
func TestB464_AuditJSON(t *testing.T) {
	if got := readinessAuditJSON(nil); got != "null" {
		t.Fatalf("nil payload → %q, want null", got)
	}
	got := readinessAuditJSON(&Readiness{Serving: true, Ready: true, HealthURL: "u"})
	if !strings.Contains(got, `"serving":true`) {
		t.Fatalf("payload JSON = %q", got)
	}
}

// ---------- the state machine (real SQLite) -----------------------------------

// b464Node seeds one cluster + one node in the given state (the b402 harness, kept
// separate so a change to one block's fixture cannot silently change the other).
func b464Node(t *testing.T, state string) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b464.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging','staging','[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state,
		                          skygate_version, joined_at, last_seen_at)
		VALUES ('node-disc-svyatoslava','skygate-staging','svyatoslava','100.64.0.20',
		        '{skygate-standby}', $1, 'v1.5.108+59fbad7', $2, $2)`,
		state, db.ActiveDialect().TimeValue(nowUTC())); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return d
}

// b464Beat performs one authenticated heartbeat carrying the given payload.
func b464Beat(t *testing.T, d *sql.DB, rd *Readiness) *HeartbeatResponse {
	t.Helper()
	inviteID, token, _, err := IssueInvite(d, DefaultClusterID, NodeRoleStandby, "svyatoslava", 24, "secret")
	if err != nil {
		t.Fatalf("IssueInvite: %v", err)
	}
	if _, err := d.Exec(`UPDATE cluster_invite SET used_by_node_id = $1 WHERE id = $2`,
		"node-disc-svyatoslava", inviteID); err != nil {
		t.Fatalf("bind invite: %v", err)
	}
	resp, err := Heartbeat(d, "secret", &HeartbeatRequest{
		NodeID: "node-disc-svyatoslava", Token: token, Version: "v1.5.108+59fbad7", Readiness: rd,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	return resp
}

func b464State(t *testing.T, d *sql.DB) string {
	t.Helper()
	var state string
	if err := d.QueryRow(`SELECT state FROM cluster_node WHERE id='node-disc-svyatoslava'`).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return state
}

// TestB464_ReadyIsDemotedWhenTheHostCannotServe is THE contract: this is the live
// false positive, reproduced. The row said `ready`; the agent reports that nothing
// answers on its own host; the state must stop saying ready.
func TestB464_ReadyIsDemotedWhenTheHostCannotServe(t *testing.T) {
	d := b464Node(t, NodeStateReady)
	resp := b464Beat(t, d, &Readiness{
		Serving:    false,
		HealthURL:  "http://127.0.0.1:8080/healthz",
		ProbeError: "dial tcp 127.0.0.1:8080: connect: connection refused",
	})
	if resp.State != NodeStateDegraded {
		t.Fatalf("state = %q, want degraded — nothing answers on that host", resp.State)
	}
	if got := b464State(t, d); got != NodeStateDegraded {
		t.Fatalf("stored state = %q, want degraded", got)
	}
	if !resp.Degraded || resp.Reason == "" {
		t.Fatalf("the hub must tell the agent its own verdict: degraded=%v reason=%q", resp.Degraded, resp.Reason)
	}
}

// TestB464_DegradedIsAudited — the transition must be explainable on the page: the
// reason lives in cluster_audit (no column carries it, B359) and /admin/cluster
// reads the last node_health row.
func TestB464_DegradedIsAudited(t *testing.T) {
	d := b464Node(t, NodeStateReady)
	b464Beat(t, d, &Readiness{Serving: true, Ready: false, HealthURL: "h", DB: "error: no db"})
	var detail string
	if err := d.QueryRow(`
		SELECT detail FROM cluster_audit
		 WHERE action='node_health' AND target_node_id='node-disc-svyatoslava'
		 ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(detail, NodeStateDegraded) || !strings.Contains(detail, "NOT READY") {
		t.Fatalf("audit detail = %q, want the to-state and the measured sentence", detail)
	}
	// The measured block must be in the row too: it is the only place the operator can
	// see WHAT was measured at that moment (db string, probe URL, build).
	if !strings.Contains(detail, `"readiness"`) {
		t.Fatalf("audit detail = %q, want the readiness block", detail)
	}
}

// TestB464_RecoveryFlipsBackToReady — the verdict is re-measured every beat, so a
// fixed host returns to ready without an operator action. This is the half that
// makes `degraded` a state and not a permanent brand.
func TestB464_RecoveryFlipsBackToReady(t *testing.T) {
	d := b464Node(t, NodeStateDegraded)
	resp := b464Beat(t, d, &Readiness{Serving: true, Ready: true, HealthURL: "h", DB: "ok"})
	if resp.State != NodeStateReady || resp.Degraded {
		t.Fatalf("state = %q degraded=%v, want ready", resp.State, resp.Degraded)
	}
	if got := b464State(t, d); got != NodeStateReady {
		t.Fatalf("stored state = %q, want ready", got)
	}
}

// TestB464_OldAgentKeepsB402 — the compatibility guarantee, in the state machine.
// A heartbeat with no payload must still clear `failed` exactly as B402 shipped it:
// the fix cannot turn a working pre-B464 deployment into a degraded one.
func TestB464_OldAgentKeepsB402(t *testing.T) {
	for _, from := range []string{NodeStateFailed, NodeStatePending} {
		t.Run(from, func(t *testing.T) {
			d := b464Node(t, from)
			resp := b464Beat(t, d, nil)
			if resp.State != NodeStateReady {
				t.Fatalf("%s + old-agent heartbeat = %q, want ready (B402 behaviour)", from, resp.State)
			}
		})
	}
}

// TestB464_DrainingOutranksTheVerdict — a verdict must never undo the operator's own
// drain. Same rule B402 established for `failed`.
func TestB464_DrainingOutranksTheVerdict(t *testing.T) {
	d := b464Node(t, NodeStateDraining)
	resp := b464Beat(t, d, &Readiness{Serving: true, Ready: true, HealthURL: "h", DB: "ok"})
	if resp.State != NodeStateDraining {
		t.Fatalf("state = %q, want draining untouched", resp.State)
	}
}

// TestB464_PendingIsNotPromotedToReadyByASilentHost — the pre-B464 machine promoted
// `pending` to `ready` on ANY authenticated heartbeat. With a verdict the promotion
// only happens when the host really serves, so an unapproved node whose agent is
// broken cannot look better than it is.
func TestB464_PendingIsNotPromotedByABrokenHost(t *testing.T) {
	d := b464Node(t, NodeStatePending)
	resp := b464Beat(t, d, &Readiness{Serving: false, HealthURL: "h", ProbeError: "refused"})
	if resp.State != NodeStateDegraded {
		t.Fatalf("state = %q, want degraded — a pending row must not be promoted on a silent host", resp.State)
	}
}

// TestB464_OldAgentSaysWhatItsReadyMeans — a pre-B464 agent sends no measured
// sentence, but the audit must not stay silent: `ready` from an agent that cannot
// report readiness is a WEAKER claim, and that difference is the whole point of the
// block. A blank reason would make the two indistinguishable in cluster_audit.
func TestB464_OldAgentSaysWhatItsReadyMeans(t *testing.T) {
	d := b464Node(t, NodeStateFailed)
	b464Beat(t, d, nil)
	var detail string
	if err := d.QueryRow(`
		SELECT detail FROM cluster_audit
		 WHERE action='node_health' AND target_node_id='node-disc-svyatoslava'
		 ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, want := range []string{`"reason":"`, "pre-B464", "not 'serving'"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("audit detail = %q, want it to contain %q (the weaker claim must be named)", detail, want)
		}
	}
}

// nowUTC is a tiny seam so the fixtures cannot drift from the product's clock
// convention (UTC everywhere).
func nowUTC() (t time.Time) { return time.Now().UTC() }
