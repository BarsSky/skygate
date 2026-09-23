// internal/headscale/ssh_key_b292_test.go — B292 (2026-09-23).
//
// The live `aro` report ("выдает ошибку при пересинхронизации") was a GREEN flash
// carrying:
//
//	Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
//	Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or
//	directory. ssh: Could not resolve hostname exit-node-vps: Name or service
//	not known approved=21
//
// Two distinct defects hide in it (a container-only key path on a native host,
// and a target that fell back to the node name because exit_servers had neither
// ssh_target nor tailscale_ip) and neither was named anywhere. These tests pin
// the preflight that names them before an ssh process is spawned.
package headscale

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSSHKeyProblem_NamesEachBlocker(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name     string
		path     string
		wantSub  string
		wantNone bool
	}{
		{"readable key is usable", keyFile, "", true},
		{"empty path", "", "no ssh key configured", false},
		{"blank path", "   ", "no ssh key configured", false},
		{"relative path", "keys/id_ed25519", "not absolute", false},
		{"missing file", filepath.Join(dir, "nope"), "ssh key not found", false},
		{"directory", sub, "is a directory", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SSHKeyProblem(c.path)
			if c.wantNone {
				if got != "" {
					t.Fatalf("SSHKeyProblem(%q) = %q, want no problem", c.path, got)
				}
				return
			}
			if !strings.Contains(got, c.wantSub) {
				t.Fatalf("SSHKeyProblem(%q) = %q, want it to contain %q", c.path, got, c.wantSub)
			}
		})
	}
}

// TestSSHKeyProblem_UnreadableKey is the "the file is there but the service
// account cannot read it" case — the one that otherwise only shows up as
// `Permission denied (publickey)` from ssh, which points at the wrong thing.
// Unix-only: mode 0000 does not block reads for root, and Windows has no
// equivalent of the owner/group/mode check at all.
func TestSSHKeyProblem_UnreadableKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode-based read restriction to construct")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root — mode 0000 does not block reads")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(key, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
	got := SSHKeyProblem(key)
	if !strings.Contains(got, "not readable") {
		t.Fatalf("SSHKeyProblem(%q) = %q, want a 'not readable' verdict", key, got)
	}
}

func TestSSHKeyFixHint_ExplainsTheContainerDefault(t *testing.T) {
	// The live path: the resolution landed on the container default on a host
	// that is not a container, so the hint must say exactly that.
	hint := SSHKeyFixHint("/ssh-sync/id_ed25519")
	if !strings.Contains(hint, "CONTAINER default") {
		t.Errorf("hint for the container default = %q, want it to name the container default", hint)
	}
	if !strings.Contains(hint, "ssh_key_path") || !strings.Contains(hint, "authorized_keys") {
		t.Errorf("hint = %q, want both the field to set and where the public key goes", hint)
	}
	// Any other path still gets the actionable half, without the container note.
	other := SSHKeyFixHint("/var/lib/skygate/ssh/id_ed25519")
	if strings.Contains(other, "CONTAINER default") {
		t.Errorf("hint for a host path = %q, must not blame the container default", other)
	}
	if !strings.Contains(other, "SKYGATE_EXIT_SSH_KEY") {
		t.Errorf("hint = %q, want the global knobs named", other)
	}
}

func TestSSHKeyState_ClassifiesEveryVerdict(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cases := []struct {
		path string
		want string
	}{
		{key, "ok"},
		{"", "unset"},
		{"relative/key", "relative"},
		{filepath.Join(dir, "absent"), "missing"},
		{dir, "directory"},
	}
	for _, c := range cases {
		if got := SSHKeyState(c.path); got != c.want {
			t.Errorf("SSHKeyState(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	// The page keys the warning banner off this predicate.
	if SSHKeyStateNeedsOperator("ok") {
		t.Error("SSHKeyStateNeedsOperator(ok) must be false — a usable key is not a warning")
	}
	for _, s := range []string{"unset", "missing", "unreadable", "directory", "relative", "inaccessible"} {
		if !SSHKeyStateNeedsOperator(s) {
			t.Errorf("SSHKeyStateNeedsOperator(%q) must be true — every non-ok state blocks the sync", s)
		}
	}
}

// TestSetAdvertisedRoutes_NamesTheKeyProblemInsteadOfRunningSSH is the
// behavioural half: a missing key must be reported WITHOUT an ssh process, and
// the message must carry both the reason and the fix.
func TestSetAdvertisedRoutes_NamesTheKeyProblemInsteadOfRunningSSH(t *testing.T) {
	c := New("http://127.0.0.1:1", "stub-key")
	_, err := c.SetAdvertisedRoutes("exit-node-vps", []string{"0.0.0.0/0"}, -1,
		"", filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("a missing key file must be refused before ssh runs")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ssh key not found") {
		t.Errorf("error = %q, want the named reason", msg)
	}
	if !strings.Contains(msg, "ssh_key_path") {
		t.Errorf("error = %q, want the fix (exit_servers.ssh_key_path / SKYGATE_EXIT_SSH_KEY)", msg)
	}
}

// TestSetAdvertisedRoutes_ContainerDefaultHintWhenKeyMissing: the exact live
// path must explain the install-kind mismatch rather than leaving the operator
// with an ssh warning.
func TestSetAdvertisedRoutes_ContainerDefaultHintWhenKeyMissing(t *testing.T) {
	c := New("http://127.0.0.1:1", "stub-key")
	// ContainerDefaultSSHKey almost certainly does not exist on the test host.
	if _, err := os.Stat(ContainerDefaultSSHKey); err == nil {
		t.Skipf("%s exists on this host — cannot exercise the missing-default path", ContainerDefaultSSHKey)
	}
	_, err := c.SetAdvertisedRoutes("exit-node-vps", []string{"0.0.0.0/0"}, -1, "", ContainerDefaultSSHKey)
	if err == nil {
		t.Fatal("want an error for the missing container default")
	}
	if !strings.Contains(err.Error(), "CONTAINER default") {
		t.Errorf("error = %q, want it to name the container default (B292)", err)
	}
}

// TestContainerDefaultSSHKey_MatchesConfig guards the one duplicated literal:
// internal/config cannot import this package, so it keeps its own copy of the
// in-container mount and the contract is "they agree".
func TestContainerDefaultSSHKey_MatchesConfig(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "config", "config.go"))
	if err != nil {
		t.Skipf("cannot read config source: %v", err)
	}
	if !strings.Contains(string(src), `headscaleContainerSSHKey = "`+ContainerDefaultSSHKey+`"`) {
		t.Errorf("internal/config's container SSH key constant no longer equals %q — update both", ContainerDefaultSSHKey)
	}
}
