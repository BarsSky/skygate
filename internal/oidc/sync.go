// Package oidc — B167 (v1.5.2) — sync OIDC config
// between skygate and headscale.
//
// Why this exists
// ---------------
// B161.1-B161.4 made skygate a working OIDC provider
// for headscale, but the operator still had to
// hand-edit headscale.conf to enable the integration
// (set the `oidc:` block) and `docker restart headscale`
// to apply. The two steps were:
//
//   1. Copy the snippet from /admin/oidc into
//      /etc/headscale/config.yaml (or wherever
//      headscale reads its config from)
//   2. Restart headscale (and wait for it to come back)
//
// That left room for a typo, a wrong redirect_uri, or
// a stale `oidc.client_secret`. B167 closes the loop:
// the operator clicks "Apply" in /admin/oidc/sync
// and the page does both steps for them.
//
// Modes (full Option C, see deploy/oidc-sync.sh for
// the authoritative list):
//
//   - docker (most common): skygate container has
//     /var/run/docker.sock mounted, the headscale
//     container is reachable via `docker restart`.
//   - systemd: headscale runs as a system service
//     (no docker). `systemctl restart headscale`.
//   - k8s: headscale is in a kubernetes deployment.
//     `kubectl rollout restart deploy/headscale -n headscale`.
//   - manual: skygate writes the headscale.conf and
//     updates its own .env, but the operator restarts
//     headscale by hand (e.g. on a separate VM).
//   - download: the page shows the generated YAML
//     and lets the operator download / copy-paste it.
//   - api: headscale 0.30+ supports a gRPC `configure
//     oidc` method. We call it via `docker exec`
//     instead of writing a file (cleaner — headscale
//     stays the source of truth).
//
// All modes share the same deploy/oidc-sync.sh script.
// This file is the Go-side wrapper that calls the
// script, parses the JSON output, and exposes a typed
// result to the admin handler.
//
// Auto-sync on init
// -----------------
// When the operator sets SKYGATE_OIDC_AUTOSYNC=true,
// the skygate container runs the sync at startup
// (after the OIDC keypair is loaded, but before the
// HTTP server starts accepting traffic). This is for
// the "headscale lives on the same VM, OIDC env
// vars are in .env" case — skygate picks them up,
// syncs headscale, and serves the new config
// without any manual Apply click. The auto-sync
// re-uses the same script (mode: auto) so the
// behavior is identical to the manual Apply.
//
// What B167 does NOT include
// --------------------------
// - External headscale via API key (headscale 0.30
//   gRPC API has a `configure oidc` call, but the
//   path requires gRPC client code we don't have;
//   we use the headscale CLI via `docker exec`
//   instead, which works for the same-machine case
//   but not for a remote headscale). A follow-up
//   could add a gRPC client.
// - headplane-side OIDC config (headplane is a
//   web UI; its OIDC config is in headplane-config
//   and is orthogonal to headscale.conf). Not in
//   scope for B167.
package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncResult is the typed result of a successful
// oidc-sync.sh invocation. The deploy script writes
// one of these as JSON on stdout; we parse it back
// here and surface the fields to the admin page.
//
// The fields mirror the JSON keys in deploy/oidc-sync.sh
// 1:1. If you add a new field to the script, add the
// same field here (the parse fails otherwise).
type SyncResult struct {
	// Ok is always true on success. The script exits
	// non-zero on failure and we report a Go error;
	// this field is mostly for forward-compat (e.g.
	// a future "ok: false" partial-success mode).
	Ok bool `json:"ok"`
	// SkygateURL is the issuer URL we synced.
	SkygateURL string `json:"skygate_url"`
	// ClientID is the OIDC client_id we synced.
	ClientID string `json:"client_id"`
	// HeadscaleConfigPath is the absolute path to
	// the headscale.conf we wrote (empty if mode
	// is "download" — no file write happened).
	HeadscaleConfigPath string `json:"headscale_config_path"`
	// ConfigBackupPath is the .pre-oidc-sync backup
	// of the headscale.conf (empty if no backup was
	// needed, e.g. first run, or download mode).
	ConfigBackupPath string `json:"config_backup_path"`
	// EnvPath is the path to skygate's .env (passed
	// in by the script, may be empty in download mode).
	EnvPath string `json:"env_path"`
	// EnvBackupPath is the backup of skygate's .env.
	EnvBackupPath string `json:"env_backup_path"`
	// OIDCBlockYAML is the generated headscale.conf
	// `oidc:` block, ready to copy-paste. Always
	// populated, even in non-download modes.
	OIDCBlockYAML string `json:"oidc_block_yaml"`
	// Mode is which restart strategy was used:
	// docker / systemd / k8s / manual / download / api.
	Mode string `json:"mode"`
	// HeadscaleRestarted is true if the script
	// successfully triggered a restart.
	HeadscaleRestarted bool `json:"headscale_restarted"`
	// HeadscaleHealthy is true if /health responded
	// 200 within RESTART_TIMEOUT seconds after restart.
	HeadscaleHealthy bool `json:"headscale_healthy"`
	// EnvUpdated is true if the script wrote new
	// OIDC_* values to skygate's .env.
	EnvUpdated bool `json:"env_updated"`
	// TestResult is the optional post-sync probe
	// (e.g. "200" if headscale could reach the
	// OIDC provider). Empty if no probe was run.
	TestResult string `json:"test_result"`
	// DurationMs is how long the sync took wall-clock.
	DurationMs int `json:"duration_ms"`
}

// SyncRequest is the input to RunSync. The fields
// match the script's positional args + the most
// important flags. Defaults are filled in by
// RunSync itself (so callers can pass zero values
// for "use the default").
type SyncRequest struct {
	// SkygateURL is the issuer URL (required).
	SkygateURL string
	// ClientID + ClientSecret are the OIDC
	// credentials (required).
	ClientID     string
	ClientSecret string
	// RedirectURIs is a comma-separated list of
	// allowed redirect URIs (required).
	RedirectURIs string
	// HeadscaleConfigPath is where the script
	// writes the new headscale.conf. Empty =
	// auto-detect (try common paths).
	HeadscaleConfigPath string
	// HeadscaleContainer is the docker container
	// name to restart (default: "headscale").
	HeadscaleContainer string
	// SkygateEnvPath is skygate's .env to update
	// (default: /home/skyadmin/skygate/.env).
	SkygateEnvPath string
	// ModeOverride is the explicit mode to use
	// (docker / systemd / k8s / manual / download
	// / api). Empty = auto-detect.
	ModeOverride string
	// DownloadOnly is true for the "download" mode
	// (no file writes, no restarts). Same as
	// ModeOverride="download" but more explicit.
	DownloadOnly bool
	// ScriptPath is the path to deploy/oidc-sync.sh.
	// Empty = auto-detect from the executable's
	// directory (works in production; tests pass
	// an explicit path).
	ScriptPath string
}

// RunSync invokes deploy/oidc-sync.sh with the
// fields in req and returns the parsed JSON result.
// The function returns an error if:
//   - the script is not found at ScriptPath
//   - the script exits non-zero (e.g. headscale
//     failed to come back healthy within the timeout)
//   - the script's stdout isn't valid JSON
//
// The function is safe for concurrent use (each
// invocation is its own subprocess).
//
// Timeout: 120 seconds (matches the script's worst
// case: 60s health wait + the time for backup +
// YAML write + .env write + restart). Callers that
// need a shorter timeout (e.g. the live "Apply"
// button) can pass a context with deadline via
// RunSyncCtx.
//
// The 120s is conservative; in the happy path
// the script finishes in <1s for download mode
// and <5s for docker mode.
func RunSync(req SyncRequest) (*SyncResult, error) {
	return RunSyncCtx(context.Background(), req)
}

// RunSyncCtx is RunSync with a context. The
// script's own timeout (RESTART_TIMEOUT=60s) is
// the upper bound for the headscale health check;
// the context is a hard kill for the whole
// subprocess (e.g. if the caller is HTTP and
// the operator navigated away).
func RunSyncCtx(ctx context.Context, req SyncRequest) (*SyncResult, error) {
	// Validate required fields
	if req.SkygateURL == "" {
		return nil, fmt.Errorf("oidc sync: SkygateURL is required")
	}
	if req.ClientID == "" {
		return nil, fmt.Errorf("oidc sync: ClientID is required")
	}
	if req.ClientSecret == "" {
		return nil, fmt.Errorf("oidc sync: ClientSecret is required")
	}
	if req.RedirectURIs == "" {
		return nil, fmt.Errorf("oidc sync: RedirectURIs is required")
	}

	// Locate the script. Production callers can
	// pass an empty ScriptPath; we walk up from the
	// executable's directory until we find the
	// deploy/oidc-sync.sh file. The walk is bounded
	// to 5 levels so a misconfigured install
	// doesn't recurse forever.
	script := req.ScriptPath
	if script == "" {
		var err error
		script, err = findSyncScript()
		if err != nil {
			return nil, fmt.Errorf("oidc sync: locate script: %w", err)
		}
	}

	// Build the args list
	args := []string{
		req.SkygateURL,
		req.ClientID,
		req.ClientSecret,
		req.RedirectURIs,
	}
	if req.ModeOverride != "" {
		args = append(args, "--mode", req.ModeOverride)
	}
	if req.DownloadOnly || req.ModeOverride == "download" {
		args = append(args, "--download-only")
	}
	if req.HeadscaleConfigPath != "" {
		args = append(args, "--headscale-config", req.HeadscaleConfigPath)
	}
	if req.HeadscaleContainer != "" {
		args = append(args, "--headscale-container", req.HeadscaleContainer)
	}
	if req.SkygateEnvPath != "" {
		args = append(args, "--skygate-env", req.SkygateEnvPath)
	}

	// Run with a 120s timeout
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, script, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("oidc sync: invoking %s (skygate=%s, client_id=%s, mode=%q)",
		script, req.SkygateURL, req.ClientID, req.ModeOverride)
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	if err != nil {
		// The script writes a JSON-ish error to stderr
		// (or just "WARN: ..." lines). We surface the
		// last 2KB of stderr so the admin page can
		// show a useful error message.
		errMsg := strings.TrimSpace(stderr.String())
		if len(errMsg) > 2048 {
			errMsg = errMsg[len(errMsg)-2048:]
		}
		return nil, fmt.Errorf("oidc sync: script failed after %s: %w (stderr: %s)",
			dur.Round(time.Millisecond), err, errMsg)
	}

	// Parse JSON result
	var res SyncResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		// Stdout should be JSON; if it's not, the
		// script is broken (we know it outputs JSON
		// from the live tests). Return the raw
		// stdout in the error so the operator can
		// see what went wrong.
		rawOut := strings.TrimSpace(stdout.String())
		if len(rawOut) > 2048 {
			rawOut = rawOut[len(rawOut)-2048:]
		}
		return nil, fmt.Errorf("oidc sync: parse JSON: %w (stdout: %s)", err, rawOut)
	}
	log.Printf("oidc sync: OK in %s (mode=%s, restarted=%v, healthy=%v)",
		dur.Round(time.Millisecond), res.Mode, res.HeadscaleRestarted, res.HeadscaleHealthy)
	return &res, nil
}

// findSyncScript locates deploy/oidc-sync.sh by
// walking up from the current executable's directory.
// In production skygate is at $REPO/skygate, so we
// expect to find $REPO/deploy/oidc-sync.sh 1 level
// up. In tests, the test binary may be in a temp
// dir; the walk covers up to 5 levels.
func findSyncScript() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 5; i++ {
		candidate := filepath.Join(dir, "deploy", "oidc-sync.sh")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		// Also try "scripts" in case the operator
		// moved it (some installs use scripts/
		// for runtime helpers).
		candidate2 := filepath.Join(dir, "scripts", "oidc-sync.sh")
		if info, err := os.Stat(candidate2); err == nil && !info.IsDir() {
			return candidate2, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("oidc-sync.sh not found within 5 levels of %s", exe)
}

// ShouldAutoSync returns true if the boot-time
// auto-sync should run. The env var is opt-in
// (default false) so the operator controls when
// skygate mutates headscale.conf — a misplaced
// auto-sync could break an existing headscale
// install if the env vars are wrong.
//
// The auto-sync is only run when:
//   1. SKYGATE_OIDC_AUTOSYNC=true (explicit opt-in)
//   2. SKYGATE_OIDC_ISSUER is non-empty (otherwise
//      there's nothing to sync)
//   3. SKYGATE_OIDC_CLIENT_SECRET is non-empty
//      (otherwise the generated headscale.conf
//      would have a blank client_secret)
//
// All three conditions prevent the most common
// "I forgot to set the env vars" footguns.
func ShouldAutoSync() bool {
	if !strings.EqualFold(os.Getenv("SKYGATE_OIDC_AUTOSYNC"), "true") {
		return false
	}
	if os.Getenv("SKYGATE_OIDC_ISSUER") == "" {
		log.Printf("oidc sync: SKYGATE_OIDC_AUTOSYNC=true but SKYGATE_OIDC_ISSUER is empty — skipping auto-sync")
		return false
	}
	if os.Getenv("SKYGATE_OIDC_CLIENT_SECRET") == "" {
		log.Printf("oidc sync: SKYGATE_OIDC_AUTOSYNC=true but SKYGATE_OIDC_CLIENT_SECRET is empty — skipping auto-sync")
		return false
	}
	return true
}
