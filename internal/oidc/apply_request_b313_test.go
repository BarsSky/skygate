// internal/oidc/apply_request_b313_test.go — B313 (v1.5.78).
//
// The request/result pair is the whole mechanism behind the panel button, so the
// properties that matter are pinned here: the request is written ATOMICALLY (the applier
// may be reading the previous one), it carries the finished block, an empty or
// non-oidc block is refused, the helper's "armed" verdict is a stat and not a guess, and
// a missing result reads as "no apply yet" rather than an error.
package oidc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteOIDCApplyRequest_B313(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_OIDC_REQUEST_PATH", filepath.Join(dir, "oidc.request.props"))
	t.Setenv("SKYGATE_OIDC_RESULT_PATH", filepath.Join(dir, "oidc.result.props"))

	block := RenderHeadscaleBlock(HeadscaleBlockValues{
		Issuer:       "https://head.example.com",
		ClientID:     "headscale",
		ClientSecret: "s3cret",
		RedirectURIs: "https://head.example.com/oidc/callback",
		SecretSet:    true,
	}, true)
	path, err := WriteOIDCApplyRequest(OIDCApplyRequest{
		Block:           block,
		RequestedBy:     "admin",
		HeadscaleConfig: "/etc/headscale/config.yaml",
	})
	if err != nil {
		t.Fatalf("WriteOIDCApplyRequest: %v", err)
	}
	if path != OIDCRequestPath() {
		t.Errorf("path = %q, want %q", path, OIDCRequestPath())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// 0600: the file carries the client secret. Windows emulates the mode bits
	// (everything reads as 0666), so the assertion is Unix-only — CI is Linux, which is
	// where the property matters.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("request mode = %o, want 600 (it carries the secret)", perm)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"REQUESTED_BY=\"admin\"",
		"HEADSCALE_CONFIG=\"/etc/headscale/config.yaml\"",
		"BLOCK_BEGIN", "BLOCK_END",
		"client_secret: s3cret",
		"issuer: https://head.example.com",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request does not contain %q:\n%s", want, body)
		}
	}
	// No temp files left behind (the writer renames, it does not copy).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".oidc.request.") {
			t.Errorf("a temp file survived the write: %s", e.Name())
		}
	}

	// An empty block and a block that is not a headscale oidc: section are both refused:
	// a request the helper cannot apply would look like a successful press.
	if _, err := WriteOIDCApplyRequest(OIDCApplyRequest{Block: "  "}); err == nil {
		t.Errorf("an empty block must be refused")
	}
	if _, err := WriteOIDCApplyRequest(OIDCApplyRequest{Block: "server_url: http://x\n"}); err == nil {
		t.Errorf("a block without oidc: must be refused")
	}
}

func TestReadOIDCApplyResult_B313(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_OIDC_RESULT_PATH", filepath.Join(dir, "oidc.result.props"))

	// Nothing yet: a normal state on a fresh install, and the page must be able to say
	// exactly that.
	if r := ReadOIDCApplyResult(); r.Present || r.OK() {
		t.Errorf("a missing result must read as not-present, got %+v", r)
	}

	body := "# skygate privileged OIDC result (B313)\nSTATUS=ok\nDETAIL=\"applied https://head.example.com to /etc/headscale/config.yaml via systemd (backup config.yaml.pre-oidc-b304.20260924)\"\nAT=\"2026-09-24T07:00:00Z\"\n"
	if err := os.WriteFile(OIDCResultPath(), []byte(body), 0o644); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	r := ReadOIDCApplyResult()
	if !r.Present || !r.OK() {
		t.Fatalf("result = %+v, want a present ok", r)
	}
	if !strings.Contains(r.Detail, "backup config.yaml.pre-oidc-b304") || r.At != "2026-09-24T07:00:00Z" {
		t.Errorf("result = %+v", r)
	}

	// A failure is reported as a failure, with its reason (that is what makes the button
	// debuggable).
	if err := os.WriteFile(OIDCResultPath(), []byte("STATUS=failed\nDETAIL=\"rolled back: headscale did not restart\"\nAT=\"t\"\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if r := ReadOIDCApplyResult(); r.OK() || !strings.Contains(r.Detail, "rolled back") {
		t.Errorf("failed result = %+v", r)
	}

	// An unreadable file (garbage) must not crash the page or claim success.
	if err := os.WriteFile(OIDCResultPath(), []byte("no key value lines here\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if r := ReadOIDCApplyResult(); r.OK() || r.Status != "unknown" {
		t.Errorf("garbage result = %+v, want present+unknown and not ok", r)
	}
}

func TestOIDCHelperArmed_B313(t *testing.T) {
	dir := t.TempDir()
	applier := filepath.Join(dir, "skygate-apply-oidc.sh")
	unit := filepath.Join(dir, "skygate-oidc.path")
	t.Setenv("SKYGATE_OIDC_APPLIER", applier)
	t.Setenv("SKYGATE_OIDC_PATH_UNIT", unit)

	if OIDCHelperArmed() {
		t.Errorf("nothing installed yet — the helper must not look armed")
	}
	if err := os.WriteFile(applier, []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatalf("seed applier: %v", err)
	}
	if OIDCHelperArmed() {
		t.Errorf("the script alone is not enough: without the path unit nothing triggers it")
	}
	if err := os.WriteFile(unit, []byte("[Path]\n"), 0o644); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	if !OIDCHelperArmed() {
		t.Errorf("script + path unit must read as armed")
	}
	if got := OIDCHelperScriptPath(); got != applier {
		t.Errorf("OIDCHelperScriptPath = %q, want %q (the fallback message must name the real path)", got, applier)
	}
}

// TestRenderHeadscaleBlock_B313: the request carries the SAME block `skygate
// oidc-export --headscale --secret` prints — the two paths cannot drift.
func TestRenderHeadscaleBlock_B313(t *testing.T) {
	block := RenderHeadscaleBlock(HeadscaleBlockValues{
		Issuer:       "https://head.example.com",
		ClientID:     "headscale",
		ClientSecret: "s3cret",
		RedirectURIs: "https://head.example.com/oidc/callback,https://other.example.com/oidc/callback",
		SecretSet:    true,
	}, true)
	for _, want := range []string{
		"oidc:\n",
		"  issuer: https://head.example.com\n",
		"  client_id: headscale\n",
		"  client_secret: s3cret\n",
		"  scope: [openid, profile, email]\n",
		"    - head.example.com\n",
		"  auto_update: true\n",
		"  strip_email_domain: true\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block does not contain %q:\n%s", want, block)
		}
	}
	// Without an explicit secret the documented placeholder is rendered instead of an
	// empty line (headscale would start with OIDC effectively unconfigured).
	noSecret := RenderHeadscaleBlock(HeadscaleBlockValues{Issuer: "https://x", ClientID: "c"}, true)
	if !strings.Contains(noSecret, "client_secret: <set-on-headscale-side>") {
		t.Errorf("a missing secret must render the placeholder:\n%s", noSecret)
	}
	// An empty redirect list keeps a usable allowed_domains (headscale rejects every
	// login with an empty one).
	if !strings.Contains(RenderHeadscaleBlock(HeadscaleBlockValues{Issuer: "https://x", ClientID: "c", SecretSet: true, ClientSecret: "s"}, true), "- head.example.com") {
		t.Errorf("an empty redirect list must keep the placeholder domain")
	}
}
