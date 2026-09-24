// internal/oidc/apply_request_b313.go — B313 (v1.5.78).
//
// THE PROBLEM THE OPERATOR REPORTED: on the native host `aro` the OIDC page still said
// "fix the env" and offered a shell script to copy-paste. The panel already holds every
// value (B290/B304), so the missing half is not configuration — it is the WRITE: with
// `ProtectSystem=strict` the skygate unit cannot touch headscale's config, and the
// restart belongs to root. The policy applier solved exactly this shape (B272.3+) and
// this file reuses it: the panel writes a DATA-ONLY request into the update directory
// and a root-owned helper (`skygate-oidc.path` → `deploy/skygate-apply-oidc.sh
// --from-request`) performs the write, restarts headscale, verifies and records the
// outcome where the page can read it.
//
// The request carries the FINISHED `oidc:` block (rendered by the same function
// `skygate oidc-export` uses), so the helper never formats YAML and the two paths cannot
// drift. It also carries the secret, which is why the file is created 0600 in a 0750
// directory and why the result file never echoes it: the result names the status and a
// reason, not the material.
package oidc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OIDC request/result file names inside the update directory.
const (
	oidcRequestFile = "oidc.request.props"
	oidcResultFile  = "oidc.result.props"
)

// updateDirForRequest is where the privileged helpers look for requests. It mirrors the
// policy applier's directory so an install has one place to watch.
func updateDirForRequest() string {
	if dir := strings.TrimSpace(os.Getenv("SKYGATE_UPDATE_DIR")); dir != "" {
		return dir
	}
	return "/var/lib/skygate/update"
}

// OIDCRequestPath is the request file the applier consumes.
//
// SKYGATE_OIDC_REQUEST_PATH overrides it (the contract test drives the real script
// against a temporary file), exactly like the policy helper's override.
func OIDCRequestPath() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_OIDC_REQUEST_PATH")); p != "" {
		return p
	}
	return filepath.Join(updateDirForRequest(), oidcRequestFile)
}

// OIDCResultPath is where the applier records its verdict.
func OIDCResultPath() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_OIDC_RESULT_PATH")); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(OIDCRequestPath()), oidcResultFile)
}

// OIDCApplyRequest is one pending apply. HeadscaleConfig may be empty: the applier then
// discovers headscale's own config path (it already knows how, and the panel is not
// required to).
type OIDCApplyRequest struct {
	HeadscaleConfig string
	Block           string
	RequestedBy     string
}

// OIDCApplyResult is what the applier recorded.
type OIDCApplyResult struct {
	Status  string // ok | failed | running
	Detail  string
	At      string
	Present bool
}

// OK reports whether the last apply succeeded.
func (r OIDCApplyResult) OK() bool { return strings.EqualFold(strings.TrimSpace(r.Status), "ok") }

// OIDCHelperArmed reports whether the privileged path exists: the applier script AND the
// systemd path unit. Only a stat — the caller uses it to choose between "applied by the
// helper" and "run this by hand", never to guess.
func OIDCHelperArmed() bool {
	applier := strings.TrimSpace(os.Getenv("SKYGATE_OIDC_APPLIER"))
	if applier == "" {
		applier = "/usr/local/lib/skygate/skygate-apply-oidc.sh"
	}
	if _, err := os.Stat(applier); err != nil {
		return false
	}
	unit := strings.TrimSpace(os.Getenv("SKYGATE_OIDC_PATH_UNIT"))
	if unit == "" {
		unit = "/etc/systemd/system/skygate-oidc.path"
	}
	_, err := os.Stat(unit)
	return err == nil
}

// OIDCHelperScriptPath names the applier for the "run it by hand" fallback message.
func OIDCHelperScriptPath() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_OIDC_APPLIER")); p != "" {
		return p
	}
	return "/usr/local/lib/skygate/skygate-apply-oidc.sh"
}

// WriteOIDCApplyRequest hands the finished block to the privileged helper.
//
// ATOMIC by construction (temp file + rename): the helper may be reading the previous
// request while a new one lands, and a reader that sees half of each document is exactly
// the failure that crash-looped headscale on the policy path (B283). The temp file is
// created in the same directory so the rename cannot cross a filesystem.
func WriteOIDCApplyRequest(req OIDCApplyRequest) (string, error) {
	block := strings.TrimSpace(req.Block)
	if block == "" {
		return "", fmt.Errorf("oidc apply: refusing to request an empty block")
	}
	if !strings.Contains(block, "oidc:") {
		return "", fmt.Errorf("oidc apply: the block does not look like a headscale oidc: section")
	}
	path := OIDCRequestPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("oidc apply: create %s: %w", dir, err)
	}
	body := fmt.Sprintf("# skygate privileged OIDC request (B313)\n"+
		"# DATA ONLY — the applier must never source this file.\n"+
		"REQUESTED_AT=%q\n"+
		"REQUESTED_BY=%q\n"+
		"HEADSCALE_CONFIG=%q\n"+
		"BLOCK_BEGIN\n%s\nBLOCK_END\n",
		time.Now().UTC().Format(time.RFC3339), req.RequestedBy, req.HeadscaleConfig, block)

	tmp, err := os.CreateTemp(dir, ".oidc.request.*.tmp")
	if err != nil {
		return "", fmt.Errorf("oidc apply: create temp request in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("oidc apply: chmod the request: %w", err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("oidc apply: write the request: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("oidc apply: sync the request: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("oidc apply: close the request: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("oidc apply: publish the request: %w", err)
	}
	return path, nil
}

// ReadOIDCApplyResult parses the applier's verdict. A missing or unreadable file is
// reported as "not present" rather than an error: "no apply has run yet" is a normal
// state on a fresh install and the page must be able to say exactly that.
func ReadOIDCApplyResult() OIDCApplyResult {
	b, err := os.ReadFile(OIDCResultPath())
	if err != nil {
		return OIDCApplyResult{}
	}
	out := OIDCApplyResult{Present: true}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "STATUS":
			out.Status = val
		case "DETAIL":
			out.Detail = val
		case "AT":
			out.At = val
		}
	}
	if out.Status == "" {
		out.Status = "unknown"
	}
	return out
}
