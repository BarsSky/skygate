// internal/feature/exit_rules/sync_b292_test.go — B292 (2026-09-23).
//
// The live `aro` report: pressing Re-sync on /admin/exit-nodes produced
//
//	Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
//	Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or
//	directory. ssh: Could not resolve hostname exit-node-vps: Name or service
//	not known approved=21
//
// The second half of that message is this package's bug: the sync resolved the
// relay's SSH target ONLY from exit_servers (ssh_target → root@<tailscale_ip> →
// ""), and that row was created by an INSERT OR IGNORE discovery pass — so an
// empty tailscale_ip column stays empty forever. The sync then fell back to the
// bare node name and died on DNS, while /admin/exit-nodes displayed the relay's
// Tailscale IP read from headscale. Here we pin the fix: the live headscale view
// is consulted when the row has nothing.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"skygate/internal/headscale"
)

func b292HeadscaleStub(t *testing.T, body string) *headscale.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/node" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c := headscale.New(srv.URL, "stub-key")
	c.SetCacheTTL(0)
	return c
}

// TestLiveExitNodeIP_PrefersIPv4 covers the address the ssh argument can use.
func TestLiveExitNodeIP_PrefersIPv4(t *testing.T) {
	hs := b292HeadscaleStub(t, `{"nodes":[
	  {"id":"1","name":"exit-node-vps","givenName":"exit-node-vps",
	   "ipAddresses":["100.64.0.1","fd7a:115c:a1e0::1"]}
	]}`)
	if got := liveExitNodeIP(hs, "exit-node-vps"); got != "100.64.0.1" {
		t.Errorf("liveExitNodeIP = %q, want the IPv4 address (an ssh target cannot be a comma list "+
			"and a bare IPv6 literal would need brackets)", got)
	}
}

// TestLiveExitNodeIP_MatchesGivenNameAndHostname: device_rules.exit_node_id
// stores whichever of the two the operator saw first.
func TestLiveExitNodeIP_MatchesGivenNameAndHostname(t *testing.T) {
	hs := b292HeadscaleStub(t, `{"nodes":[
	  {"id":"1","name":"exit-node-vps.tailnet.ts.net","givenName":"exit-node-vps",
	   "ipAddresses":["100.64.0.1"]}
	]}`)
	if got := liveExitNodeIP(hs, "exit-node-vps"); got != "100.64.0.1" {
		t.Errorf("liveExitNodeIP(givenName) = %q, want 100.64.0.1", got)
	}
	if got := liveExitNodeIP(hs, "EXIT-NODE-VPS"); got != "100.64.0.1" {
		t.Errorf("liveExitNodeIP must match case-insensitively (got %q)", got)
	}
	if got := liveExitNodeIP(hs, "some-other-relay"); got != "" {
		t.Errorf("liveExitNodeIP(unknown) = %q, want \"\" (no invented address)", got)
	}
}

// TestLiveExitNodeIP_NoAddressMeansNoTarget: a node without addresses must not
// produce a bogus target — the caller then reports the missing ssh_target
// instead of sending ssh after a hostname.
func TestLiveExitNodeIP_NoAddressMeansNoTarget(t *testing.T) {
	hs := b292HeadscaleStub(t, `{"nodes":[{"id":"1","name":"exit-node-vps","givenName":"exit-node-vps","ipAddresses":[]}]}`)
	if got := liveExitNodeIP(hs, "exit-node-vps"); got != "" {
		t.Errorf("liveExitNodeIP = %q, want \"\" when headscale reports no address", got)
	}
}

// TestLiveExitNodeIP_HeadscaleDownIsNotFatal: an unreachable headscale must
// degrade to the old behaviour (no target), never panic or invent an address.
func TestLiveExitNodeIP_HeadscaleDownIsNotFatal(t *testing.T) {
	hs := b292HeadscaleStub(t, `not json`)
	if got := liveExitNodeIP(hs, "exit-node-vps"); got != "" {
		t.Errorf("liveExitNodeIP on a broken headscale = %q, want \"\"", got)
	}
	if got := liveExitNodeIP(nil, "exit-node-vps"); got != "" {
		t.Errorf("liveExitNodeIP(nil) = %q, want \"\"", got)
	}
	if got := liveExitNodeIP(hs, ""); got != "" {
		t.Errorf("liveExitNodeIP(empty) = %q, want \"\"", got)
	}
}
