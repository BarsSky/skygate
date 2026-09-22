// B272.3 — the privileged policy helper request.
//
// # WHY THIS EXISTS
//
// With `policy.mode: file` headscale keeps its ACL in a file that skygate must
// extend (a new per-device tag needs a `tagOwners` entry before headscale will
// accept the tag at all). On a native install the skygate unit runs with
//
//	ProtectSystem=strict
//	ReadWritePaths=${data_dir} ${etc_dir}
//
// so `/etc/headscale` is READ-ONLY for it by design — the live host answered
//
//	write policy file /etc/headscale/policy.hujson:
//	  open /etc/headscale/policy.hujson.skygate.tmp: read-only file system
//
// even after the file itself had been made group-writable. Two ways out were
// possible: widen the service's mount namespace (grant the portal write access
// to another service's configuration — rejected) or reuse the project's existing
// privilege split (B261): the unprivileged service writes a DATA-ONLY request
// into its own data dir, a root-owned path unit fires a root-owned service
// which applies it. This file implements the client half.
//
// The request file carries the policy text and the target path and nothing else:
// the applier never sources it, never runs anything from it, and validates both
// fields (absolute path, must already exist, sane size) before writing.
package headscale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrPolicyHelperUnavailable means the privileged policy helper is not
// installed / not armed on this host. Callers turn that into an actionable
// error (install the helper, or apply the policy by hand) instead of a silent
// no-op.
var ErrPolicyHelperUnavailable = errors.New("privileged policy helper is not installed")

// PolicyRequestPath returns the request file the root-owned applier watches.
// Default: <SKYGATE_UPDATE_DIR>/policy.request.props. The request is written
// next to the self-update request on purpose — both are "privileged action
// requests" and inherit the same ownership model.
func PolicyRequestPath() string {
	if p := os.Getenv("SKYGATE_POLICY_REQUEST_PATH"); p != "" {
		return p
	}
	dir := os.Getenv("SKYGATE_UPDATE_DIR")
	if dir == "" {
		if state := os.Getenv("SKYGATE_UPDATE_STATE_PATH"); state != "" {
			dir = filepath.Join(filepath.Dir(state), "update")
		}
	}
	if dir == "" {
		dir = "/var/lib/skygate/update"
	}
	return filepath.Join(dir, "policy.request.props")
}

// PolicyHelperArmed reports whether the privileged helper is installed: the
// applier script and the systemd path unit must both exist. It is a cheap
// stat-only check so callers can decide whether to attempt the handoff.
func PolicyHelperArmed() bool {
	applier := getenvDefault("SKYGATE_POLICY_APPLIER", "/usr/local/lib/skygate/skygate-apply-policy.sh")
	if _, err := os.Stat(applier); err != nil {
		return false
	}
	unit := getenvDefault("SKYGATE_POLICY_PATH_UNIT", "/etc/systemd/system/skygate-policy.path")
	_, err := os.Stat(unit)
	return err == nil
}

// RequestPolicyApply hands the policy to the privileged helper (B272.3).
//
// Returns ErrPolicyHelperUnavailable when the helper is not installed, so the
// caller can produce the "apply it by hand" message. Any other error means the
// handoff itself failed.
func RequestPolicyApply(path, policy string) error {
	if path == "" {
		return fmt.Errorf("policy helper: empty target path")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("policy helper: policy path %q is not absolute", path)
	}
	if policy == "" {
		return fmt.Errorf("policy helper: empty policy body")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("policy helper: target %s is not readable: %w", path, err)
	}
	if !PolicyHelperArmed() {
		return ErrPolicyHelperUnavailable
	}
	// B283 (2026-09-22): validate BEFORE handing anything over, and hand it
	// over ATOMICALLY.
	//
	// The root applier reads this request file line by line, and the old code
	// rewrote it in place with os.WriteFile (truncate + write). When a second
	// policy request landed while the applier was mid-read, the reader kept its
	// file offset into the NEW content and stitched the head of one document to
	// the tail of another: the live host wrote a 7869-byte policy at 18:26 that
	// was byte-for-byte the size of the saved snapshot, valid in the database,
	// and unparseable on disk (`hujson: line 302, column 23: invalid character
	// ']' after object name`) — headscale then crash-looped 248 times and every
	// device disappeared from the portal. A rename(2) cannot splice: a reader
	// sees either the whole old file or the whole new one.
	if _, perr := PolicyJSON(policy); perr != nil {
		return fmt.Errorf("policy helper: refusing to hand over a policy that does not parse: %w", perr)
	}
	req := PolicyRequestPath()
	dir := filepath.Dir(req)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("policy helper: create %s: %w", dir, err)
	}
	body := fmt.Sprintf("# skygate privileged policy request (B272.3)\n# data only — never sourced by the applier\nPOLICY_PATH=%q\nREQUESTED_AT=%q\nPOLICY_BEGIN\n%s\nPOLICY_END\n",
		path, time.Now().UTC().Format(time.RFC3339), policy)
	tmp, err := os.CreateTemp(dir, "policy.request.*.tmp")
	if err != nil {
		return fmt.Errorf("policy helper: create temp request in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("policy helper: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("policy helper: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("policy helper: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, req); err != nil {
		return fmt.Errorf("policy helper: rename %s -> %s: %w", tmpName, req, err)
	}
	return nil
}
