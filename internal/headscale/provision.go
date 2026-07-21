// Package headscale — per-user headscale provisioning (v0.23.0 Phase 1).
//
// 2026-07-21: v0.23.0 Phase 1.
//
// The v0.12.0 design added per-user control plane capability:
//   - portal_users.headscale_url (TEXT)
//   - portal_users.headscale_api_key_enc (TEXT, AES-GCM encrypted)
//   - HSForUser(uid) routes API calls to the right plane
//   - /admin/users/{id}/plane (manual URL+key entry form)
//
// v0.23.0 Phase 1 closes the operational gap that v0.12.0 left
// open: the manual form requires the operator to:
//   1. ssh to the VM
//   2. set up a headscale container (or use a remote one)
//   3. create a user + API key
//   4. paste the URL + key into the form
//
// Provision is the one-click shortcut for the common case
// (a fresh per-user headscale container on the same host):
//   1. admin clicks "Provision per-user headscale" on
//      /admin/users/{id}/plane
//   2. handler calls ProvisionUser(username, uid)
//   3. shell out to deploy/headscale-users/headscale-bootstrap.sh
//   4. script creates the container + user + API key,
//      returns JSON
//   5. handler parses JSON, encrypts the API key with
//      SKYGATE_SECRET_KEY, persists via db.SetUserHeadscaleConfig
//   6. handler invalidates the per-url HSForUser cache
//
// Decommission is the reverse: teardown the container, clear
// the DB row. Data on disk is preserved (moved to a sibling
// `.decommissioned-<ts>` dir) so the operator can recover.
//
// Errors are wrapped with enough context for the admin UI to
// surface them ("bootstrap script failed: ...") without
// needing a separate log lookup.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BootstrapScriptPath is where the bootstrap script lives INSIDE
// the skygate container. The script is bind-mounted from the host
// at /home/skyadmin/skygate/deploy/headscale-users/. Tests can
// override this via a var.
var BootstrapScriptPath = "/usr/local/bin/headscale-bootstrap.sh"

// DeprovisionScriptPath mirrors BootstrapScriptPath for the
// teardown path.
var DeprovisionScriptPath = "/usr/local/bin/headscale-deprovision.sh"

// ProvisionResult is the JSON output of headscale-bootstrap.sh.
// Field names match the script's JSON exactly — the Go side parses
// them 1:1. Adding a field is non-breaking; renaming is.
//
// 2026-07-21: v0.23.0 Phase 1.
type ProvisionResult struct {
	Username        string `json:"username"`
	Container       string `json:"container"`
	URL             string `json:"url"`
	APIKey          string `json:"api_key"`
	HTTPPort        int    `json:"http_port"`
	GRPCPort        int    `json:"grpc_port"`
	MetricsPort     int    `json:"metrics_port"`
	BaseDomain      string `json:"base_domain"`
	HeadscaleUserID int64  `json:"headscale_user_id"`
}

// runScript invokes the given shell script via `bash <path>
// <args...>`. We always go through bash explicitly (rather
// than relying on the kernel's shebang handling) so the
// code works identically on:
//   - Linux production (the actual deployment target)
//   - Windows dev (CI, ops engineers running `go test`
//     locally with git-bash on WSL or Cygwin)
//
// On Windows, `exec.Command(path, args...)` fails with
// "%1 is not a valid Win32 application" because the kernel
// doesn't recognize the shebang. Wrapping in bash avoids
// that without needing a separate Windows-specific code
// path. The bootstrap script itself is portable bash, so
// there's no Linux-specific syntax that would break on
// Windows.
//
// We also translate Windows paths (C:\foo\bar) to git-bash's
// /mnt/c/foo/bar form because bash on Windows in Cygwin/WSL
// mode mounts Windows drives under /mnt/<letter>. The
// production deployment is Linux so the path passes through
// unchanged; only the Windows test path triggers the
// translation.
func runScript(path string, args ...string) ([]byte, error) {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) >= 2 && path[1] == ':' {
		// C:/foo/bar → /mnt/c/foo/bar
		path = "/mnt/" + strings.ToLower(string(path[0])) + path[2:]
	}
	return exec.Command("bash", append([]string{path}, args...)...).Output()
}

// ProvisionUser runs the bootstrap script for the given user and
// returns the parsed result. The script does the heavy lifting
// (container creation, user + API key, port allocation) — this
// function is a thin shell wrapper.
//
// Errors are wrapped with the script's stderr in the message so
// the admin UI can surface them directly. If the script is missing
// (operator hasn't bind-mounted it) the error mentions that
// specifically — common misconfiguration in fresh deployments.
func ProvisionUser(username string, uid int64) (*ProvisionResult, error) {
	if username == "" {
		return nil, fmt.Errorf("provision: empty username")
	}
	if uid <= 0 {
		return nil, fmt.Errorf("provision: invalid uid %d", uid)
	}
	// script takes <username> <uid>
	out, err := runScript(BootstrapScriptPath, username, fmt.Sprintf("%d", uid))
	if err != nil {
		// exec.ExitError carries stderr; for other errors
		// (e.g. binary not found) Output is empty.
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("bootstrap script failed (exit %d): %s",
				ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("bootstrap script: %w (script path: %s)", err, BootstrapScriptPath)
	}
	// Parse the JSON. The script's last line is a complete JSON
	// object; stdout may also contain other output (e.g. docker
	// compose progress) so we find the JSON object explicitly.
	body := strings.TrimSpace(string(out))
	start := strings.Index(body, "{")
	if start < 0 {
		return nil, fmt.Errorf("bootstrap script: no JSON found in output (raw: %s)", body)
	}
	end := strings.LastIndex(body, "}")
	if end < 0 || end <= start {
		return nil, fmt.Errorf("bootstrap script: malformed JSON in output (raw: %s)", body)
	}
	body = body[start : end+1]
	var result ProvisionResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("bootstrap script: parse JSON: %w (raw: %s)", err, body)
	}
	if result.URL == "" || result.APIKey == "" || result.Container == "" {
		return nil, fmt.Errorf("bootstrap script: missing required fields (url=%q api_key=%q container=%q)",
			result.URL, result.APIKey, result.Container)
	}
	return &result, nil
}

// DecommissionUser tears down the per-user headscale container.
// Errors are returned verbatim from the script.
//
// The script PRESERVES the per-user data directory (moved to
// `.decommissioned-<ts>`) so the operator can recover manually.
// This is intentional: a wrong-button click shouldn't lose data.
func DecommissionUser(username string) error {
	if username == "" {
		return fmt.Errorf("decommission: empty username")
	}
	_, err := runScript(DeprovisionScriptPath, username)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("decommission script failed (exit %d): %s",
				ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return fmt.Errorf("decommission script: %w (script path: %s)", err, DeprovisionScriptPath)
	}
	return nil
}

// IsProvisioned reports whether a per-user headscale container
// exists for the given username. Used by the /admin/users/{id}/plane
// page to decide which button to render (Provision vs Decommission).
//
// Implemented as a `docker ps -a` lookup — cheap, no headscale
// round-trip required. The check is intentionally lenient: a
// container that's been stopped is still "provisioned" (decommission
// is a separate explicit action).
func IsProvisioned(username string) bool {
	if username == "" {
		return false
	}
	containerName := "headscale-" + username
	cmd := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == containerName {
			return true
		}
	}
	return false
}

// CleanScriptPath is a small helper that returns the absolute
// form of a script path. Used by tests to construct the path
// the shim should live at on Windows (which has a different
// short-path layout than the production /usr/local/bin mount).
func CleanScriptPath(path string) string {
	return filepath.Clean(path)
}
