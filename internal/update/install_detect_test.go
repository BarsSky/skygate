// install_detect_test.go — 2026-09-18: pins the install-kind detection
// contract used by /admin/update.
//
// WHY
// ---
// The operator reported that self-update works under Docker but not on a
// native install. The detection itself was fine — the AUTOMATED updater is
// Docker-only (the "not yet implemented" branches in
// internal/feature/admin/update.go). But the detection feeds the manual
// steps the page prints, so a wrong answer sends the operator to the wrong
// procedure entirely, which is what the ordering bug fixed here did:
// container markers are now checked BEFORE /run/systemd/system, because
// skygate runs in a container ON a systemd host and a visible
// /run/systemd/system (bind-mount of /run, permissive image) used to flip
// the answer to InstallSystemd.
//
// The filesystem half cannot be tested portably (it stats absolute paths),
// so this pins the half operators actually rely on — the env override used
// on air-gapped hosts — plus the String() values that end up on the page
// and in audit rows.
package update

import "testing"

func TestDetectInstallKindOverride(t *testing.T) {
	cases := []struct {
		env  string
		want InstallKind
	}{
		{"docker", InstallDocker},
		{"Docker", InstallDocker},
		{"docker-compose", InstallDocker},
		{"compose", InstallDocker},
		{"systemd", InstallSystemd},
		{"SYSTEMCTL", InstallSystemd},
		{"bare", InstallBare},
		{"binary", InstallBare},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SKYGATE_INSTALL_KIND", tc.env)
			if got := DetectInstallKind(); got != tc.want {
				t.Errorf("DetectInstallKind() with SKYGATE_INSTALL_KIND=%q = %v, want %v",
					tc.env, got, tc.want)
			}
		})
	}
}

// An unrecognised override value must fall through to the filesystem
// detection, not silently claim docker: a confidently wrong answer sends
// the operator to the wrong update procedure.
func TestDetectInstallKindUnknownOverrideValueFallsThrough(t *testing.T) {
	t.Setenv("SKYGATE_INSTALL_KIND", "definitely-not-a-kind")
	got := DetectInstallKind()
	want := detectInstallKindFilesystem()
	if got != want {
		t.Errorf("unknown override value gave %v, want the filesystem answer %v", got, want)
	}
}

func TestInstallKindStringStable(t *testing.T) {
	// These strings are shown on /admin/update and written to audit rows,
	// so they are part of the operator-facing contract.
	for _, tc := range []struct {
		kind InstallKind
		want string
	}{
		{InstallDocker, "docker"},
		{InstallSystemd, "systemd"},
		{InstallBare, "bare"},
		{InstallUnknown, "unknown"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("InstallKind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}
