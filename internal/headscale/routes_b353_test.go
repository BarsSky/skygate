// routes_b353_test.go — B353 (2026-10-06).
//
// The transport may now reach a relay THROUGH a peer relay (`ssh -J`), because the
// portal can be on the tailnet and still have no path to a healthy relay (live:
// emilia homed on DERP region 28, which this site's network cannot reach, while the
// peer relay karolina answered it fine — see relay_transport_jump_b353.go).
//
// These tests pin the argv itself: one place composes it, the direct form keeps
// every B266 hardening property, and the jump form cannot be injected through the
// hop value. They do NOT run ssh.
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
	if strings.Contains(joined, "-J") {
		t.Errorf("a direct connection must not carry -J: %v", args)
	}
	if args[len(args)-3] != "--" {
		t.Errorf("`--` must still terminate option parsing before the host: %v", args)
	}
}

// TestB353_JumpArgvUsesDashJAndNotProxyCommand: OpenSSH refuses both at once —
// measured on OpenSSH 10.3p1: `Cannot specify -J with ProxyCommand`. Emitting both
// would turn the new fallback into a guaranteed failure, which is worse than the
// timeout it is meant to fix.
func TestB353_JumpArgvUsesDashJAndNotProxyCommand(t *testing.T) {
	args := buildSetAdvertisedRoutesArgv("/k", "root@100.64.0.3", "22", "tailscale set --y", "root@100.64.0.2:18022")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-J root@100.64.0.2:18022") {
		t.Fatalf("the hop must reach ssh as -J: %v", args)
	}
	if strings.Contains(joined, "ProxyCommand") {
		t.Fatalf("ssh refuses -J together with ProxyCommand: %v", args)
	}
	if !strings.Contains(joined, "-o IdentitiesOnly=yes") {
		t.Errorf("the jump form must keep identity pinning: %v", args)
	}
	if args[len(args)-3] != "--" || args[len(args)-2] != "root@100.64.0.3" {
		t.Errorf("the target must stay after `--`: %v", args)
	}
	// The hop is a value, never an option: it sits AFTER -J, so `--` still protects
	// the host, and the caller validates the value before this point.
	i := 0
	for ; i < len(args); i++ {
		if args[i] == "-J" {
			break
		}
	}
	if i+1 >= len(args) || args[i+1] != "root@100.64.0.2:18022" {
		t.Fatalf("-J must be followed by the hop value: %v", args)
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

// TestB353_JumpFailureNamesTheHop: "Operation timed out" straight from the portal and
// the same message through a peer relay have different fixes, so the error must say
// which path was attempted.
func TestB353_JumpFailureNamesTheHop(t *testing.T) {
	// ssh is not run: the key preflight fails first, and that is the error we read.
	c := New("http://127.0.0.1:1", "stub-key")
	_, err := c.SetAdvertisedRoutes("emilia", []string{"0.0.0.0/0"}, -1, "root@100.64.0.3", "/definitely/missing/key", "root@100.64.0.2:18022")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "through root@100.64.0.2:18022") {
		t.Fatalf("the key preflight error must not claim a hop was used: %v", err)
	}
	// The pure builder is what carries the hop into the message we care about; assert
	// the wiring rather than spawning ssh.
	args := buildSetAdvertisedRoutesArgv("/k", "root@100.64.0.3", "", "cmd", "root@100.64.0.2")
	if !strings.Contains(strings.Join(args, " "), "-J root@100.64.0.2") {
		t.Fatalf("hop missing from the argv: %v", args)
	}
}
