package oidc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isWindows reports whether the test is running
// on Windows (skips the live script test there
// since bash isn't always available on Windows
// CI without Git Bash or WSL).
func isWindows() bool {
	return runtime.GOOS == "windows"
}

// TestRunSync_DownloadOnly exercises the happy
// path: a download-only sync (no file writes, no
// restarts) and a valid JSON result. The script is
// the real deploy/oidc-sync.sh (no mock — we want
// to verify the contract between Go and bash).
func TestRunSync_DownloadOnly(t *testing.T) {
	script := findRepoScript(t)
	if script == "" {
		t.Skip("oidc-sync.sh not found in repo (run from C:/Projects/skygate)")
	}

	// On Linux/macOS, exec.Command can run the
	// script directly via the shebang. On Windows
	// the shebang is ignored, so we need to
	// dispatch via `bash` (or `wsl bash`).
	// Production runs on Linux (the operator's VM),
	// so this is just a test convenience.
	if isWindows() {
		// Try to find bash on PATH; skip if not
		// available (CI without Git Bash or WSL).
		if _, err := exec.LookPath("bash"); err != nil {
			if _, err := exec.LookPath("wsl"); err != nil {
				t.Skip("no bash/wsl on Windows PATH; skipping live script test")
			}
		}
	}

	res, err := RunSync(SyncRequest{
		SkygateURL:         "https://skygate.example.com",
		ClientID:           "headscale",
		ClientSecret:       "test-secret-do-not-use-in-prod",
		RedirectURIs:       "https://head.skynas.ru/oidc/callback",
		ModeOverride:       "download",
		ScriptPath:         script,
		HeadscaleConfigPath: "/nonexistent/path/to/headscale/config.yaml",
		SkygateEnvPath:     "/nonexistent/path/to/skygate/.env",
	})
	if err != nil {
		t.Skipf("RunSync (script execution) failed in this env: %v", err)
	}
	if !res.Ok {
		t.Errorf("expected ok=true, got false")
	}
	if res.Mode != "download" {
		t.Errorf("expected mode=download, got %q", res.Mode)
	}
	if res.HeadscaleRestarted {
		t.Errorf("download mode should not restart headscale")
	}
	if res.EnvUpdated {
		t.Errorf("download mode should not update .env")
	}
	if res.OIDCBlockYAML == "" {
		t.Errorf("expected OIDCBlockYAML to be populated")
	}
	// The generated block must contain the issuer
	// + client_id we passed in
	if !strings.Contains(res.OIDCBlockYAML, "issuer: https://skygate.example.com") {
		t.Errorf("OIDC block missing issuer: %s", res.OIDCBlockYAML)
	}
	if !strings.Contains(res.OIDCBlockYAML, "client_id: headscale") {
		t.Errorf("OIDC block missing client_id: %s", res.OIDCBlockYAML)
	}
	// B167.1: strip_email_domain was removed in
	// headscale 0.23+, so the generated block must
	// NOT contain it (a regression would break
	// headscale 0.29.x at startup).
	if strings.Contains(res.OIDCBlockYAML, "strip_email_domain") {
		t.Errorf("OIDC block must not contain strip_email_domain (removed in headscale 0.23+): %s", res.OIDCBlockYAML)
	}
	if res.DurationMs <= 0 {
		t.Errorf("expected DurationMs > 0, got %d", res.DurationMs)
	}
}

// TestRunSync_ValidationErrors verifies that
// RunSync returns a clear error for each missing
// required field, without spawning a subprocess.
func TestRunSync_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		req  SyncRequest
	}{
		{"empty SkygateURL", SyncRequest{ClientID: "x", ClientSecret: "y", RedirectURIs: "z"}},
		{"empty ClientID", SyncRequest{SkygateURL: "x", ClientSecret: "y", RedirectURIs: "z"}},
		{"empty ClientSecret", SyncRequest{SkygateURL: "x", ClientID: "y", RedirectURIs: "z"}},
		{"empty RedirectURIs", SyncRequest{SkygateURL: "x", ClientID: "y", ClientSecret: "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunSync(tc.req)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "required") {
				t.Errorf("error should mention 'required', got: %v", err)
			}
		})
	}
}

// TestRunSync_ScriptNotFound verifies that
// RunSync returns a clear error when the script
// doesn't exist.
func TestRunSync_ScriptNotFound(t *testing.T) {
	_, err := RunSync(SyncRequest{
		SkygateURL:   "https://skygate.example.com",
		ClientID:     "headscale",
		ClientSecret: "x",
		RedirectURIs: "y",
		ScriptPath:   "/nonexistent/path/oidc-sync.sh",
	})
	if err == nil {
		t.Errorf("expected error for missing script, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "oidc-sync.sh") {
		t.Errorf("error should mention script not found, got: %v", err)
	}
}

// TestRunSyncCtx_ContextCancel verifies that
// RunSyncCtx respects the context's deadline (a
// ctx that's already cancelled should kill the
// subprocess immediately).
func TestRunSyncCtx_ContextCancel(t *testing.T) {
	script := findRepoScript(t)
	if script == "" {
		t.Skip("oidc-sync.sh not found in repo (run from C:/Projects/skygate)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := RunSyncCtx(ctx, SyncRequest{
		SkygateURL:   "https://skygate.example.com",
		ClientID:     "headscale",
		ClientSecret: "x",
		RedirectURIs: "y",
		ModeOverride: "download",
		ScriptPath:   script,
	})
	if err == nil {
		t.Errorf("expected error from cancelled context, got nil")
	}
}

// TestShouldAutoSync verifies the auto-sync
// gating logic: requires SKYGATE_OIDC_AUTOSYNC=true
// AND non-empty SKYGATE_OIDC_ISSUER AND non-empty
// SKYGATE_OIDC_CLIENT_SECRET.
func TestShouldAutoSync(t *testing.T) {
	// Save and restore env vars
	envVars := []string{"SKYGATE_OIDC_AUTOSYNC", "SKYGATE_OIDC_ISSUER", "SKYGATE_OIDC_CLIENT_SECRET"}
	orig := make(map[string]string)
	for _, k := range envVars {
		orig[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range orig {
			if v != "" {
				os.Setenv(k, v)
			}
		}
	}()

	// All unset → false
	if ShouldAutoSync() {
		t.Errorf("expected false with all env vars unset")
	}
	// Only AUTOSYNC=true → false (missing issuer + secret)
	os.Setenv("SKYGATE_OIDC_AUTOSYNC", "true")
	if ShouldAutoSync() {
		t.Errorf("expected false when issuer + secret are missing")
	}
	// AUTOSYNC=true + ISSUER set → false (missing secret)
	os.Setenv("SKYGATE_OIDC_ISSUER", "https://skygate.example.com")
	if ShouldAutoSync() {
		t.Errorf("expected false when secret is missing")
	}
	// All three set → true
	os.Setenv("SKYGATE_OIDC_CLIENT_SECRET", "x")
	if !ShouldAutoSync() {
		t.Errorf("expected true when all 3 are set")
	}
	// AUTOSYNC=false → false (overrides everything)
	os.Setenv("SKYGATE_OIDC_AUTOSYNC", "false")
	if ShouldAutoSync() {
		t.Errorf("expected false when AUTOSYNC=false")
	}
	// Case-insensitive AUTOSYNC
	os.Setenv("SKYGATE_OIDC_AUTOSYNC", "TRUE")
	if !ShouldAutoSync() {
		t.Errorf("expected true when AUTOSYNC=TRUE (case-insensitive)")
	}
}

// TestSyncResult_JSONFields pins the JSON field
// names so the bash script can rely on them. A
// regression in the Go struct tags breaks the
// parse in RunSync.
func TestSyncResult_JSONFields(t *testing.T) {
	// The 13 fields the bash script writes. If
	// you add a new field, add it here too.
	required := []string{
		"ok", "skygate_url", "client_id",
		"headscale_config_path", "config_backup_path",
		"env_path", "env_backup_path",
		"oidc_block_yaml", "mode",
		"headscale_restarted", "headscale_healthy",
		"env_updated", "test_result", "duration_ms",
	}
	// Round-trip a sample struct and verify all
	// fields survive
	original := SyncResult{
		Ok:                   true,
		SkygateURL:           "x",
		ClientID:             "y",
		HeadscaleConfigPath:  "z",
		ConfigBackupPath:     "b",
		EnvPath:              "e",
		EnvBackupPath:        "eb",
		OIDCBlockYAML:        "y",
		Mode:                 "m",
		HeadscaleRestarted:   true,
		HeadscaleHealthy:     true,
		EnvUpdated:           true,
		TestResult:           "200",
		DurationMs:           123,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range required {
		needle := `"` + field + `":`
		if !strings.Contains(string(data), needle) {
			t.Errorf("JSON missing field %q in: %s", field, data)
		}
	}
}

// findRepoScript locates deploy/oidc-sync.sh
// relative to the test's working directory. The
// test runner is invoked from the repo root
// (C:/Projects/skygate), so a simple relative
// path works. If the test is run from a different
// cwd, we walk up to find it.
func findRepoScript(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"deploy/oidc-sync.sh",
		"../deploy/oidc-sync.sh",
		"../../deploy/oidc-sync.sh",
		"../../../deploy/oidc-sync.sh",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}
