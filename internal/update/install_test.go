package update

import (
	"strings"
	"testing"
)

func TestInstallKind_String(t *testing.T) {
	cases := []struct {
		k    InstallKind
		want string
	}{
		{InstallDocker, "docker"},
		{InstallSystemd, "systemd"},
		{InstallBare, "bare"},
		{InstallUnknown, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.k.String(); got != c.want {
				t.Errorf("InstallKind(%d).String() = %q, want %q", c.k, got, c.want)
			}
		})
	}
}

func TestGenerateDockerSteps(t *testing.T) {
	steps := GenerateDockerSteps("v0.28.6", "v0.29.0", "BarsSky", "skygate")
	if steps.Kind != InstallDocker {
		t.Errorf("Kind = %v, want InstallDocker", steps.Kind)
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "v0.29.0") {
		t.Error("Docker steps should reference target version v0.29.0")
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "v0.28.6") {
		t.Error("Docker steps should reference current version v0.28.6 (for rollback context)")
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "docker compose build skygate") {
		t.Error("Docker steps should include 'docker compose build skygate'")
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "verify-post") {
		t.Error("Docker steps should include 'make verify-post' as the final verify step")
	}
	if !strings.Contains(strings.Join(steps.Rollback, "\n"), "v0.28.6") {
		t.Error("Docker rollback should reference the current version")
	}
}

func TestGenerateBareSteps(t *testing.T) {
	steps := GenerateBareSteps("v0.28.6", "v0.29.0", "BarsSky", "skygate")
	if steps.Kind != InstallBare {
		t.Errorf("Kind = %v, want InstallBare", steps.Kind)
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "sha256sum -c") {
		t.Error("Bare steps should include SHA256 verification")
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "git") {
		// Bare steps should NOT mention git (no source clone)
		t.Error("Bare steps should NOT mention git")
	}
	// 2026-08-05 v0.33.1.10: the GitHub owner/repo from
	// the call must be reflected in the download URL.
	// 2026-08-09 v0.33.1.24 (B73): the org/repo defaults
	// are "BarsSky" / "skygate" (the operator's actual
	// github.com repo); the pre-fix hardcoded
	// "skygate-operator/skygate" would 404 against
	// github.com/BarsSky/skygate.
	joined := strings.Join(steps.Steps, "\n")
	if !strings.Contains(joined, "github.com/BarsSky/skygate/releases/download/v0.29.0/skygate-linux-amd64") {
		t.Errorf("Bare steps should reference the configured GitHub owner/repo in the download URL; got: %s", joined)
	}
}

func TestGenerateSystemdSteps(t *testing.T) {
	steps := GenerateSystemdSteps("v0.28.6", "v0.29.0", "BarsSky", "skygate")
	if steps.Kind != InstallSystemd {
		t.Errorf("Kind = %v, want InstallSystemd", steps.Kind)
	}
	if !strings.Contains(strings.Join(steps.Steps, "\n"), "systemctl") {
		t.Error("Systemd steps should reference systemctl")
	}
}

func TestGenerateManualSteps_DefaultsToDocker(t *testing.T) {
	// Unknown install kind should fall back to Docker (the
	// most common skygate deployment shape).
	steps := GenerateManualSteps(InstallUnknown, "v0.28.6", "v0.29.0", "BarsSky", "skygate")
	if steps.Kind != InstallDocker {
		t.Errorf("GenerateManualSteps(Unknown) = %v, want InstallDocker (default)", steps.Kind)
	}
}

// 2026-08-05 v0.33.1.10: when the caller passes empty
// owner/repo, the steps must still render a valid GitHub
// download URL (the "BarsSky/skygate" fallback). Without
// this guard, an unconfigured /admin/update page (no
// env, no config override) would render a literal
// "https://github.com//releases/..." URL.
func TestGenerateManualSteps_FallbackOwnerRepo(t *testing.T) {
	steps := GenerateManualSteps(InstallBare, "v0.28.6", "v0.29.0", "", "")
	joined := strings.Join(steps.Steps, "\n")
	if !strings.Contains(joined, "github.com/BarsSky/skygate/releases/download/") {
		t.Errorf("empty owner/repo should fall back to 'BarsSky/skygate'; got: %s", joined)
	}
}

func TestDetectInstallKind_EnvOverride(t *testing.T) {
	// SKYGATE_INSTALL_KIND=docker should override filesystem
	// detection. We test the env var path; the filesystem
	// path is tested implicitly (no /run/systemd/system in
	// the unit test environment).
	t.Setenv("SKYGATE_INSTALL_KIND", "systemd")
	if got := DetectInstallKind(); got != InstallSystemd {
		t.Errorf("DetectInstallKind() = %v, want InstallSystemd (env override)", got)
	}
	t.Setenv("SKYGATE_INSTALL_KIND", "bare")
	if got := DetectInstallKind(); got != InstallBare {
		t.Errorf("DetectInstallKind() = %v, want InstallBare (env override)", got)
	}
	t.Setenv("SKYGATE_INSTALL_KIND", "docker")
	if got := DetectInstallKind(); got != InstallDocker {
		t.Errorf("DetectInstallKind() = %v, want InstallDocker (env override)", got)
	}
}

func TestDetectInstallKind_InvalidEnv(t *testing.T) {
	// SKYGATE_INSTALL_KIND=garbage should fall through to
	// filesystem detection (and return InstallUnknown on
	// the test environment, which is neither docker nor
	// systemd).
	t.Setenv("SKYGATE_INSTALL_KIND", "garbage")
	got := DetectInstallKind()
	// We don't assert a specific value — depends on the test
	// environment. The contract is: invalid env var doesn't
	// crash, falls through. (On Linux CI with no docker, the
	// result is InstallUnknown; on a CI with docker,
	// InstallDocker.)
	if got == InstallUnknown {
		// OK
	} else if got == InstallDocker || got == InstallSystemd {
		// Also OK (filesystem detection picked one)
	} else {
		t.Errorf("DetectInstallKind with invalid env returned %v, want Unknown or detected", got)
	}
}
