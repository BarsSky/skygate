// internal/feature/admin/exit_nodes_b292_test.go — B292 (2026-09-23).
//
// The live `aro` report came through the per-row "Re-sync" button:
//
//	Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
//	Warning: Identity file … not accessible … approved=21
//
// and the template rendered it in the GREEN alert box, because
// PostAdminExitNodeSync always redirected with ?ok=. These tests pin the two
// page-side halves of the fix: the ok/err decision, and the per-row SSH-key
// verdict the table now shows before the operator presses anything.
package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/config"
	"skygate/internal/headscale"
)

func TestExitSyncFailed_B292(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		// The live shape: ssh failed, headscale approve still ran.
		{"ssh error with approve count", "ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519): Warning: Identity file not accessible approved=21", true},
		{"approve error", "ssh=ok approve=err=headscale: 500", true},
		{"json error shape", "error=sync not wired", true},
		{"both ok", "ssh=ok approved=21", false},
		// The normal state of a relay whose routes nobody approved yet — NOT a
		// failure (this is why the decision cannot be "approved=0 → error").
		{"approved zero", "ssh=ok approved=0", false},
		{"info", "info=no IP/subnet rules configured", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitSyncFailed(c.msg); got != c.want {
				t.Errorf("exitSyncFailed(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

// TestEffectiveExitSSHKeyPath_B292: the row's own key wins over the global
// default, and an empty row value falls back to the default the process actually
// resolved (which on a native install is now <data dir>/ssh/id_ed25519 instead of
// the container-only /ssh-sync path).
func TestEffectiveExitSSHKeyPath_B292(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	svc := &Service{Cfg: &config.Config{SSHKeyPath: key}}
	if got := svc.effectiveExitSSHKeyPath(""); got != key {
		t.Errorf("effectiveExitSSHKeyPath(\"\") = %q, want the global default %q", got, key)
	}
	if got := svc.effectiveExitSSHKeyPath("   "); got != key {
		t.Errorf("effectiveExitSSHKeyPath(whitespace) = %q, want the global default %q", got, key)
	}
	rowKey := filepath.Join(dir, "row-key")
	if got := svc.effectiveExitSSHKeyPath(rowKey); got != rowKey {
		t.Errorf("effectiveExitSSHKeyPath(row) = %q, want the ROW value %q (a per-relay key must win)", got, rowKey)
	}
	// No Config at all (a partially wired Service in a test or an early boot):
	// the row value still works and the empty case stays empty rather than
	// inventing a path.
	bare := &Service{}
	if got := bare.effectiveExitSSHKeyPath(rowKey); got != rowKey {
		t.Errorf("effectiveExitSSHKeyPath without Cfg = %q, want %q", got, rowKey)
	}
	if got := bare.effectiveExitSSHKeyPath(""); got != "" {
		t.Errorf("effectiveExitSSHKeyPath without Cfg and no row value = %q, want \"\"", got)
	}

	// The composition the page uses: a usable key produces no warning, and the
	// container default on this host produces the named blocker + the fix.
	if state := headscale.SSHKeyState(svc.effectiveExitSSHKeyPath("")); state != "ok" {
		t.Errorf("state for a readable key = %q, want ok", state)
	}
	state := headscale.SSHKeyState(headscale.ContainerDefaultSSHKey)
	if state == "ok" {
		t.Skipf("%s exists on this host — cannot exercise the unusable case", headscale.ContainerDefaultSSHKey)
	}
	note := headscale.SSHKeyProblem(headscale.ContainerDefaultSSHKey) + " — " + headscale.SSHKeyFixHint(headscale.ContainerDefaultSSHKey)
	if !strings.Contains(note, "CONTAINER default") {
		t.Errorf("page note = %q, want it to explain the container default", note)
	}
}

// TestExitNodesPageRendersSSHKeyWarning_B292 is the template contract: the
// banner and the per-row badge must exist, and the banner must name the two
// fields that fix it (the operator's complaint was "он даёт ошибку" with no way
// to see why).
func TestExitNodesPageRendersSSHKeyWarning_B292(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "handlers", "templates", "admin", "exit_nodes.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	src := string(tmpl)
	for _, want := range []string{
		"{{if .SSHKeyBlocked}}",          // the banner is gated on the count
		"exit_nodes.ssh_key.banner",      // its title
		"exit_nodes.ssh_key.banner_help", // and the fix
		"{{if ne .SSHKeyState \"ok\"}}",  // the per-row badge
		".EffectiveSSHKeyPath",           // showing WHICH path is unusable
		".SSHKeyNote",                    // with the reason as the tooltip
	} {
		if !strings.Contains(src, want) {
			t.Errorf("exit_nodes.html no longer contains %q — the SSH-key blocker is invisible again", want)
		}
	}
}
