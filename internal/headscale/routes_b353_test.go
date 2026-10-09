// routes_b353_test.go — B353 (2026-10-06).
//
// The transport may now reach a relay THROUGH a peer relay, because the portal can
// be on the tailnet and still have no path to a healthy relay (live: emilia homed on
// DERP region 28, which this site's network cannot reach, while the peer relay
// karolina answered it fine — see relay_transport_jump_b353.go).
//
// These tests pin the argv itself. They do NOT run ssh — but the argv they pin was
// RUN before it was pinned, because the obvious form is a trap: `ssh -J <hop>` looks
// right and does not work from the container (OpenSSH's implicit jump connection gets
// neither the identity nor the host-key policy), so the hop is spelled out as an
// explicit `ssh -W` ProxyCommand. The measured live commands are quoted in
// jumpProxyCommand's comment.
package headscale

import (
	"strings"
	"testing"
)

// TestB353_DirectArgvKeepsTheHardening: adding the jump must not weaken the path
// that every existing deployment uses.
func TestB353_DirectArgvKeepsTheHardening(t *testing.T) {
	args := buildSetAdvertisedRoutesArgv("/ssh-sync/skygate_sync", "root@100.64.0.2", "18022", "tailscale set --x", "")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-o ProxyCommand=none", "-o IdentitiesOnly=yes", "-o BatchMode=yes", "-p 18022", "-- root@100.64.0.2 tailscale set --x"} {
		if !strings.Contains(joined, want) {
			t.Errorf("direct argv lost %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "-W '[%h]:%p'") {
		t.Errorf("a direct connection must not tunnel through anything: %v", args)
	}
	// B370: the forwarded spec is built from the target the outer ssh gets, not
	// from ssh's own `%h` expansion — that expansion carries the BRACKETS of an
	// IPv6 target and produced `[[fd7a:…]]:18022`, which ssh refuses.
	if strings.Contains(joined, "%h") || strings.Contains(joined, "%p") {
		t.Errorf("the ProxyCommand must not use ssh's %%h/%%p expansions any more: %v", args)
	}
	if args[len(args)-3] != "--" {
		t.Errorf("`--` must still terminate option parsing before the host: %v", args)
	}
}

// TestB353_JumpArgvSpellsOutTheHop: the hop connection must carry the SAME identity
// and host-key policy as the outer one, or it answers
// "Permission denied (publickey,password)" (measured live, OpenSSH 10.3p1) and the
// fallback becomes a second way to fail.
func TestB353_JumpArgvSpellsOutTheHop(t *testing.T) {
	args := buildSetAdvertisedRoutesArgv("/k", "root@100.64.0.3", "22", "tailscale set --y", "root@100.64.0.2:18022")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-o ProxyCommand=ssh -W '100.64.0.3:22' -i /k") {
		t.Fatalf("the hop must be an explicit ssh -W ProxyCommand carrying the identity: %v", args)
	}
	if !strings.Contains(joined, "-p 18022 -- root@100.64.0.2") {
		t.Fatalf("the hop must carry its OWN port and end after `--`: %v", args)
	}
	if strings.Contains(joined, "ProxyCommand=none") {
		t.Fatalf("a jump argv must not also disable the proxy command: %v", args)
	}
	if strings.Count(joined, "-i /k") != 2 {
		t.Fatalf("both legs must use the management identity: %v", args)
	}
	if strings.Count(joined, "-o IdentitiesOnly=yes") != 2 {
		t.Fatalf("both legs must pin the identity: %v", args)
	}
	if strings.Count(joined, "StrictHostKeyChecking=accept-new") != 2 {
		t.Fatalf("both legs must accept a new host key (an implicit -J hop does not inherit this): %v", args)
	}
	if !strings.HasSuffix(joined, "-o IdentitiesOnly=yes -p 22 -- root@100.64.0.3 tailscale set --y") {
		t.Fatalf("the target must stay after `--` with its own port: %v", args)
	}
}

// TestB353_HopWithoutAPortUsesSSHDefault: a relay reached on 22 must not gain a
// stray `-p ""`.
func TestB353_HopWithoutAPortUsesSSHDefault(t *testing.T) {
	args := buildSetAdvertisedRoutesArgv("/k", "root@100.64.0.3", "", "cmd", "root@100.64.0.4")
	proxy := ""
	for i, a := range args {
		if strings.HasPrefix(a, "ProxyCommand=") {
			proxy = a
			_ = i
		}
	}
	if !strings.HasSuffix(proxy, "-- root@100.64.0.4") || strings.Contains(proxy, "-p ") {
		t.Fatalf("a hop on the default port must not carry -p: %q", proxy)
	}
}

// TestB353_UnsafeJumpHopIsRefusedBeforeSSH: the hop comes from the same
// operator-writable table as ssh_target, so it gets the same shape check — and it
// must fire before any process starts.
func TestB353_UnsafeJumpHopIsRefusedBeforeSSH(t *testing.T) {
	c := New("http://127.0.0.1:1", "stub-key")
	_, err := c.SetAdvertisedRoutes("emilia", []string{"0.0.0.0/0"}, -1, "root@100.64.0.3", "/definitely/missing/key", "-oProxyCommand=sh -c id")
	if err == nil {
		t.Fatal("an option-injecting hop must be refused")
	}
	if !strings.Contains(err.Error(), "jump hop") {
		t.Fatalf("the refusal must name the jump hop, got %q", err.Error())
	}
	// A well-formed hop is not the thing that gets rejected: the missing key is.
	_, err = c.SetAdvertisedRoutes("emilia", []string{"0.0.0.0/0"}, -1, "root@100.64.0.3", "/definitely/missing/key", "root@100.64.0.2:18022")
	if err == nil || strings.Contains(err.Error(), "jump hop") {
		t.Fatalf("a valid hop must pass the shape check, got %v", err)
	}
}

// TestB353_UnquotableKeyPathRefusesTheJump: the ProxyCommand is a SHELL string that
// ssh runs with `sh -c`, and the key path goes inside it. A path that would need
// quoting is refused with a named reason rather than silently turning the jump into
// a direct connection the caller asked to avoid.
func TestB353_UnquotableKeyPathRefusesTheJump(t *testing.T) {
	if jumpProxyCommandAllowed("/ssh-sync/skygate_sync") != true {
		t.Fatalf("the deployment's own key path must be allowed")
	}
	for _, bad := range []string{"", "   ", "/keys/my key", "/keys/$(id)", "/keys/a;rm -rf /", "/keys/`id`", "/keys/a\"b"} {
		if jumpProxyCommandAllowed(bad) {
			t.Errorf("key path %q must not be quoted into a ProxyCommand", bad)
		}
	}
	if got := jumpProxyCommandAllowed("/keys/ok-1_2.3@host"); !got {
		t.Errorf("an ordinary absolute path must stay allowed, got %v", got)
	}
}

// TestB353_JumpFailuresCannotBeSilent: the errors that name a hop are the difference
// between "the target is down" and "the path to it is", so the hop must appear in
// what the caller sees.
func TestB353_JumpFailuresCannotBeSilent(t *testing.T) {
	// A valid hop with an unusable key: the key preflight runs, and the message must
	// not pretend a hop carried anything.
	c := New("http://127.0.0.1:1", "stub-key")
	_, err := c.SetAdvertisedRoutes("emilia", []string{"0.0.0.0/0"}, -1, "root@100.64.0.3", "/definitely/missing/key", "root@100.64.0.2:18022")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "through root@100.64.0.2:18022") {
		t.Fatalf("the key preflight error must not claim a hop was used: %v", err)
	}
	// The hop wiring itself: the argv handed to ssh names the hop.
	args := buildSetAdvertisedRoutesArgv("/k", "root@100.64.0.3", "", "cmd", "root@100.64.0.2")
	if !strings.Contains(strings.Join(args, " "), "-- root@100.64.0.2") {
		t.Fatalf("hop missing from the argv: %v", args)
	}
}
