// internal/headscale/ssh_key.go — B292 (2026-09-23).
//
// The SSH private key the exit-node sync uses is the one thing that can make
// every advertised-routes push fail while the headscale approve step still
// succeeds — so the operator saw a GREEN flash carrying raw `ssh` stderr:
//
//	Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
//	Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or
//	directory. ssh: Could not resolve hostname exit-node-vps: Name or service
//	not known approved=21
//
// Two different defects are hidden in that line, and neither is named:
//
//  1. `/ssh-sync/id_ed25519` is the CONTAINER default (docker-compose binds the
//     operator's ~/.ssh there). On a native/systemd/bare install the path cannot
//     exist, and nothing said so.
//  2. `Could not resolve hostname exit-node-vps` means the target was the bare
//     NODE NAME, i.e. exit_servers had neither ssh_target nor tailscale_ip —
//     while /admin/exit-nodes displayed the relay's Tailscale IP (100.64.0.1)
//     read from headscale. The page and the sync disagreed.
//
// This file owns the preflight that turns both into a named, actionable error
// BEFORE an ssh process is spawned. The checks are pure filesystem probes so
// they are cheap, unit-testable, and usable from the admin page as a per-row
// badge (see internal/feature/admin/exit_nodes.go).
package headscale

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContainerDefaultSSHKey is the in-container key path docker-compose.yml binds
// (the operator's ~/.ssh). It is the correct default ONLY inside the container.
const ContainerDefaultSSHKey = "/ssh-sync/id_ed25519"

// SSHKeyProblem reports why the private key at path cannot be used, or "" when
// it looks usable (absolute path, regular file, readable).
//
// The reasons are deliberately distinct so the operator can tell "I never set
// one" from "I set the wrong path" from "the file is there but unreadable by
// the service account":
//
//	""                                     — usable
//	"no ssh key configured"                — no per-row and no global default
//	"ssh key path is not absolute (…)"     — a relative path resolves against CWD
//	"ssh key not found: <path>"            — the file does not exist
//	"ssh key path is a directory: <path>"  — a directory, not a key
//	"ssh key not readable: <path> (…)"     — exists but the process cannot read it
//	"ssh key not accessible: <path> (…)"   — stat failed for another reason
func SSHKeyProblem(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return "no ssh key configured"
	}
	if !sshKeyPathIsAbs(p) {
		return fmt.Sprintf("ssh key path is not absolute (%s)", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("ssh key not found: %s", p)
		}
		return fmt.Sprintf("ssh key not accessible: %s (%v)", p, err)
	}
	if fi.IsDir() {
		return fmt.Sprintf("ssh key path is a directory: %s", p)
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Sprintf("ssh key not readable: %s (%v)", p, err)
	}
	_ = f.Close()
	return ""
}

// SSHKeyFixHint returns the actionable half of SSHKeyProblem: what to set and
// where the matching public key has to live. It also explains the container
// default, because `/ssh-sync/id_ed25519` on a non-container host is the single
// most common way operators meet this error.
func SSHKeyFixHint(keyPath string) string {
	p := strings.TrimSpace(keyPath)
	if isContainerDefaultSSHKey(p) {
		return fmt.Sprintf("%s is the CONTAINER default (docker-compose binds the operator's ~/.ssh there) — "+
			"outside the container set exit_servers.ssh_key_path for this relay (or SKYGATE_EXIT_SSH_KEY globally) "+
			"to the host path of the private key, and put its public key in the relay's ~/.ssh/authorized_keys", p)
	}
	return "set exit_servers.ssh_key_path for this relay (or SKYGATE_EXIT_SSH_KEY globally) to the host path of " +
		"the private key, and put its public key in the relay's ~/.ssh/authorized_keys"
}

// sshKeyPathIsAbs reports whether p is an absolute key path.
//
// Deliberately not just filepath.IsAbs: the deployments this guards are Linux
// (the docker image is linux/amd64-only and the native install kinds are
// systemd/openrc/bare), so a leading "/" is absolute regardless of the platform
// the binary was BUILT on. On Windows filepath.IsAbs("/ssh-sync/id_ed25519") is
// false, which would relabel the container default as an operator typo and hide
// the far more useful "this path does not exist here" verdict.
func sshKeyPathIsAbs(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/")
}

// isContainerDefaultSSHKey reports whether p is the in-container mount path.
// Compares both the raw and the cleaned form because filepath.Clean rewrites
// separators on Windows ("/ssh-sync/id_ed25519" → `\ssh-sync\id_ed25519`).
func isContainerDefaultSSHKey(p string) bool {
	return p == ContainerDefaultSSHKey || filepath.Clean(p) == filepath.Clean(ContainerDefaultSSHKey)
}

// SSHKeyState classifies a key path for the /admin/exit-nodes per-row badge.
// "ok" means the next sync can use it; everything else names the blocker.
func SSHKeyState(path string) string {
	problem := SSHKeyProblem(path)
	switch {
	case problem == "":
		return "ok"
	case problem == "no ssh key configured":
		return "unset"
	case strings.HasPrefix(problem, "ssh key not found"):
		return "missing"
	case strings.HasPrefix(problem, "ssh key not readable"):
		return "unreadable"
	case strings.HasPrefix(problem, "ssh key path is a directory"):
		return "directory"
	case strings.HasPrefix(problem, "ssh key path is not absolute"):
		return "relative"
	default:
		return "inaccessible"
	}
}

// SSHKeyStateNeedsOperator reports whether a state returned by SSHKeyState
// blocks the sync (i.e. the page must warn instead of rendering a healthy row).
func SSHKeyStateNeedsOperator(state string) bool {
	return state != "ok"
}
