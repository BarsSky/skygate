package admin

// v0.33.1.32 — B84: /admin/telegram "Set as egress relay" uses the
// B81 chain (operator override → root@<tailscale_ip> → "") for the
// SSH target, instead of the legacy relay.Hostname fallback.
//
// Background:
//   2026-08-09 operator report: clicking "Set as egress relay" for
//   emilia on the live VM (after v0.33.1.29 B81 + the v0.33.1.31 B83
//   .env + sshKeyPath fixes) returned:
//     "SSH на emilia не удался: ssh emilia (key
//      /ssh-sync/skygate_sync): ssh: Could not resolve hostname
//      emilia: Try again"
//   The B83 key-path fix is working (the key path is now correctly
//   populated), but the SSH target is the headscale-given hostname
//   "emilia" instead of the Tailscale IP "100.64.0.3". The
//   /admin/exit-nodes/sync path uses the B81 chain
//   (operator-override → root@<tailscale_ip> → "") since v0.33.1.29,
//   but the /admin/telegram egress handler still had the legacy
//   `relay.Hostname` fallback. B84 fixes this by switching the
//   telegram handler to LookupExitServerSSHTarget (the B81 helper),
//   so the empty-ssh_target case resolves to root@<tailscale_ip>
//   just like the sync path does.
//
// This test pins the contract: the audit log written by the handler
// must contain host=root@<tailscale_ip>, not host=<hostname>. The
// audit log is the operator-visible artifact (the
// "relay=emilia host=root@100.64.0.3 ssh=err=..." line in the DB)
// that confirms the B84 fix is wired.

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/headscale"
)

// seedExitServerWithTailscaleIP inserts an enabled exit_servers row
// with a non-empty tailscale_ip. Mirrors seedExitServer but allows
// the caller to also control tailscale_ip (seedExitServer hard-codes
// it to "100.64.100.10" — the skygate-host-1 default — which
// doesn't match the B84 chain semantics when the B81 helper looks
// up the IP).
func seedExitServerWithTailscaleIP(t *testing.T, d *sql.DB, nodeID, hostname, sshTarget, sshKey, tailscaleIP string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO exit_servers(node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, description, enabled, accept_routes)
		 VALUES (?, ?, ?, ?, ?, ?, 1, 0)`,
		nodeID, hostname, tailscaleIP, sshTarget, sshKey, "test relay",
	)
	if err != nil {
		t.Fatalf("seed exit_servers %s: %v", nodeID, err)
	}
}

// TestHandleTelegramSetEgress_B84SSHTargetChain pins the B84 contract:
// when the operator clicks "Set as egress relay" for a node with an
// empty exit_servers.ssh_target and a non-empty
// exit_servers.tailscale_ip, the handler must use the B81 chain's
// "root@<tailscale_ip>" fallback for the SSH target (not the
// headscale-given hostname like "emilia"). The pre-B84 handler used
// the legacy relay.Hostname fallback, which the `ssh` CLI couldn't
// resolve in most setups.
//
// The audit log entry written by the handler (visible to operators
// in the DB) is the artifact that confirms the B84 fix is wired:
// the `host=` field must contain "root@100.64.0.3" (the Tailscale
// IP), not "emilia" (the hostname).
//
// This test takes ~10s because it exercises the real SSH call (the
// SSH will fail since there's no real relay at 100.64.0.3 in the
// test env, but the failure path writes the audit log with the
// resolved target — that's exactly what we want to verify). Tests
// that pin the helper directly (without the SSH round-trip) live
// in internal/db/exit_servers_test.go (TestLookupExitServerSSHTarget_*
// family — OperatorOverrideWins, FallsBackToTailscaleIP,
// BothEmptyReturnsEmpty, NotFoundReturnsEmpty, PicksFirstIPFromList).
func TestHandleTelegramSetEgress_B84SSHTargetChain(t *testing.T) {
	s := newTestService(t)
	// Seed: emilia (id=3) with empty ssh_target + tailscale_ip="100.64.0.3".
	// This is the exact shape of the live data post-v0.33.1.30 B82
	// (where the operator applied tag:exit-node to emilia/karolina/
	// sharlotta and the B81 chain kicks in when ssh_target is empty).
	seedExitServerWithTailscaleIP(t, s.DB, "3", "emilia", "", "/ssh-sync/skygate_sync", "100.64.0.3")

	// Provide a real *headscale.Client. The headscale.BaseURL doesn't
	// matter here — SetAdvertisedRoutes shells out to `ssh` directly,
	// not via the headscale API. We just need a non-nil client so the
	// handler doesn't short-circuit with "headscale client не
	// инициализирован" before reaching the SSH call.
	s.HSGlobalFn = func() *headscale.Client { return headscale.New("http://127.0.0.1:9999", "fake-api-key") }

	csrfCookie, _ := issueTelegramCSRF(t)
	form := url.Values{"node_id": {"3"}}
	w := invokeEgressAction(t, s, csrfCookie, "set_egress", form)
	// 303 redirect with err= (SSH failed because 100.64.0.3 doesn't
	// actually have an SSH server in the test env). The exact
	// SSH-failure text doesn't matter for B84 — what matters is the
	// audit log.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}

	// Inspect the audit log. The handler wrote
	// "relay=emilia host=<sshTarget> ssh=err ip=..." where
	// <sshTarget> is the B84-resolved target.
	var detail string
	err := s.DB.QueryRow(
		`SELECT detail FROM audit_log WHERE action='telegram_egress_set' ORDER BY id DESC LIMIT 1`,
	).Scan(&detail)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if !strings.Contains(detail, "host=root@100.64.0.3") {
		t.Errorf("B84: audit log host= must contain the B81-chain Tailscale-IP fallback. got: %s\n"+
			"(pre-B84 the handler used relay.Hostname (\"emilia\"), which the ssh CLI cannot resolve. "+
			"B84 switches to LookupExitServerSSHTarget so the empty-ssh_target case resolves to "+
			"root@<tailscale_ip> — the same chain /admin/exit-nodes/sync uses since v0.33.1.29.)",
			detail)
	}
	if strings.Contains(detail, "host=emilia ") || strings.HasSuffix(detail, "host=emilia") {
		t.Errorf("B84: audit log host= must NOT be the legacy relay.Hostname fallback (\"emilia\"). got: %s", detail)
	}
}

// TestHandleTelegramSetEgress_B84OperatorOverrideWins pins the other
// half of the B84 contract: when the operator has set a non-empty
// exit_servers.ssh_target (the operator-override priority 1 in the
// B81 chain), that override must win — NOT the B81 Tailscale-IP
// fallback. The pre-B84 handler always used the stored ssh_target
// verbatim, which was correct for this case; B84 must preserve that
// behavior (it only changes the empty-ssh_target fallback).
func TestHandleTelegramSetEgress_B84OperatorOverrideWins(t *testing.T) {
	s := newTestService(t)
	// Seed: emilia with ssh_target="root@karolina.example.com:18022"
	// (a non-default-port operator override). tailscale_ip is
	// present but should be IGNORED because the operator's
	// override wins.
	seedExitServerWithTailscaleIP(t, s.DB, "3", "emilia", "root@karolina.example.com:18022", "/ssh-sync/k", "100.64.0.3")
	s.HSGlobalFn = func() *headscale.Client { return headscale.New("http://127.0.0.1:9999", "fake") }

	csrfCookie, _ := issueTelegramCSRF(t)
	form := url.Values{"node_id": {"3"}}
	w := invokeEgressAction(t, s, csrfCookie, "set_egress", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	var detail string
	if err := s.DB.QueryRow(
		`SELECT detail FROM audit_log WHERE action='telegram_egress_set' ORDER BY id DESC LIMIT 1`,
	).Scan(&detail); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if !strings.Contains(detail, "host=root@karolina.example.com:18022") {
		t.Errorf("B84: operator override must win (priority 1 in B81 chain). got: %s", detail)
	}
	if strings.Contains(detail, "host=root@100.64.0.3") {
		t.Errorf("B84: B81 Tailscale-IP fallback must NOT override the operator's stored ssh_target. got: %s", detail)
	}
}
