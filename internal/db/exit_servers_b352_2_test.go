// exit_servers_b352_2_test.go — B352.2 (2026-10-05).
//
// The "Use Tailscale IP" button on /admin/exit-nodes must write the relay's TAILNET
// address, and it could not: it called LookupExitServerSSHTarget, which is the sync
// path's "effective target" resolver and returns an operator-set `ssh_target` FIRST.
// For the exact row the button exists for — an ssh_target pointing at a public address
// that the deployment cannot reach — it therefore returned that same address, reported
// «SSH target set to Tailscale IP: root@<the public IP>» and wrote it back unchanged.
//
// Live (2026-10-05): emilia's ssh_target was its public VPS address, geo-blocked from the
// deployment; the button was a no-op and the row kept the unreachable target.
package db

import (
	"testing"
)

func TestTailscaleSSHTargetFor_IgnoresTheOperatorTarget_B352_2(t *testing.T) {
	_, d, err := OpenWithDialect("sqlite:" + t.TempDir() + "/b352_2.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, accept_routes, enabled)
	                     VALUES ('3', 'emilia', '100.64.0.3,fd7a:115c:a1e0::3', 'root@203.0.113.9', '/ssh-sync/skygate_sync', 1, 1)`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}

	// The button's resolver: the TAILNET address, whatever ssh_target says.
	got, err := TailscaleSSHTargetFor(d, "emilia")
	if err != nil {
		t.Fatalf("TailscaleSSHTargetFor: %v", err)
	}
	if got != "root@100.64.0.3" {
		t.Errorf("TailscaleSSHTargetFor = %q, want root@100.64.0.3 — the button must not echo the operator's public target", got)
	}

	// The sync path's resolver keeps its own (correct) precedence: an explicit
	// ssh_target wins, because that is where the operator said the sync should connect.
	effective, err := LookupExitServerSSHTarget(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if effective != "root@203.0.113.9" {
		t.Errorf("LookupExitServerSSHTarget = %q, want root@203.0.113.9 (an explicit target must still win on the sync path)", effective)
	}

	// A non-default ssh port must survive on the tailnet target too (karolina's shape).
	if _, err := d.Exec(`UPDATE exit_servers SET ssh_port = '18022' WHERE hostname = 'emilia'`); err != nil {
		t.Fatalf("set ssh_port: %v", err)
	}
	got, err = TailscaleSSHTargetFor(d, "emilia")
	if err != nil {
		t.Fatalf("TailscaleSSHTargetFor after port: %v", err)
	}
	if got != "root@100.64.0.3:18022" {
		t.Errorf("TailscaleSSHTargetFor with a port = %q, want root@100.64.0.3:18022", got)
	}

	// No tailscale_ip yet → "" (the handler turns that into a clear message instead of
	// writing a malformed "root@").
	if _, err := d.Exec(`UPDATE exit_servers SET tailscale_ip = '' WHERE hostname = 'emilia'`); err != nil {
		t.Fatalf("clear tailscale_ip: %v", err)
	}
	got, err = TailscaleSSHTargetFor(d, "emilia")
	if err != nil {
		t.Fatalf("TailscaleSSHTargetFor without an IP: %v", err)
	}
	if got != "" {
		t.Errorf("TailscaleSSHTargetFor without tailscale_ip = %q, want empty", got)
	}
}
