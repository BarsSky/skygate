// 2026-08-04 v0.33.1: regression test for the "ssh: Could not
// resolve hostname 193.233.130.178:18022" bug. The pre-v0.33.1
// SetAdvertisedRoutes used `ssh user@host:port cmd` which
// the `ssh` command line does NOT understand (the
// `user@host:port` shorthand is ssh_config-only — on the
// CLI `ssh` interprets the trailing `:18022` as part of the
// hostname and tries to resolve it via DNS, which fails).
// The fix is splitSSHTarget, which separates (host, port)
// and uses `-p port` explicitly. These tests pin the
// parsing contract so a future refactor that breaks the
// v0.33.1 operator-visible behaviour (per-row
// user@host:port targets stored in exit_servers.ssh_target)
// gets caught at PR time.

package headscale

import "testing"

func TestSplitSSHTarget_UserHostPort(t *testing.T) {
	host, port := splitSSHTarget("root@193.233.130.178:18022")
	if host != "root@193.233.130.178" {
		t.Errorf("host: got %q want %q", host, "root@193.233.130.178")
	}
	if port != "18022" {
		t.Errorf("port: got %q want %q", port, "18022")
	}
}

func TestSplitSSHTarget_UserHost(t *testing.T) {
	host, port := splitSSHTarget("root@karolina")
	if host != "root@karolina" {
		t.Errorf("host: got %q want %q", host, "root@karolina")
	}
	if port != "" {
		t.Errorf("port: should be empty, got %q", port)
	}
}

func TestSplitSSHTarget_BareHost(t *testing.T) {
	host, port := splitSSHTarget("karolina")
	if host != "karolina" {
		t.Errorf("host: got %q want %q", host, "karolina")
	}
	if port != "" {
		t.Errorf("port: should be empty, got %q", port)
	}
}

func TestSplitSSHTarget_PortOnly(t *testing.T) {
	host, port := splitSSHTarget("karolina:2222")
	if host != "karolina" {
		t.Errorf("host: got %q want %q", host, "karolina")
	}
	if port != "2222" {
		t.Errorf("port: got %q want %q", port, "2222")
	}
}

func TestSplitSSHTarget_Empty(t *testing.T) {
	host, port := splitSSHTarget("")
	if host != "" || port != "" {
		t.Errorf("empty input: got (%q, %q) want (\"\", \"\")", host, port)
	}
}

func TestSplitSSHTarget_StandardPort22(t *testing.T) {
	host, port := splitSSHTarget("root@relay.example.com:22")
	if host != "root@relay.example.com" || port != "22" {
		t.Errorf("got (%q, %q) want (\"root@relay.example.com\", \"22\")", host, port)
	}
}
