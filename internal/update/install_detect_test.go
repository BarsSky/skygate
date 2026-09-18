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
		{"openrc", InstallOpenRC},
		{"rc-service", InstallOpenRC},
		{"Alpine", InstallOpenRC},
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

// The filesystem half used to be untestable (it stats absolute paths).
// 2026-09-18 (B262): statExisting is now injectable, so the ordering
// contract — container markers first, then systemd, then OpenRC — is pinned
// instead of assumed. Getting this wrong on Alpine meant /admin/update said
// "could not detect install kind" and refused to update at all.
func TestDetectInstallKindFilesystemOrder(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  InstallKind
	}{
		{"docker marker wins over everything", []string{"/.dockerenv", "/run/systemd/system", "/run/openrc"}, InstallDocker},
		{"podman marker wins over openrc", []string{"/run/.containerenv", "/run/openrc"}, InstallDocker},
		{"systemd host", []string{"/run/systemd/system"}, InstallSystemd},
		{"openrc host (alpine)", []string{"/run/openrc"}, InstallOpenRC},
		{"systemd beats a stray openrc marker", []string{"/run/systemd/system", "/run/openrc"}, InstallSystemd},
		{"nothing → unknown", nil, InstallUnknown},
	}
	orig := statExisting
	t.Cleanup(func() { statExisting = orig })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			have := map[string]bool{}
			for _, p := range tc.paths {
				have[p] = true
			}
			statExisting = func(p string) bool { return have[p] }
			defer func() { statExisting = orig }()
			if got := detectInstallKindFilesystem(); got != tc.want {
				t.Errorf("detectInstallKindFilesystem() with %v = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

func TestInstallKindIsNative(t *testing.T) {
	for _, kind := range []InstallKind{InstallSystemd, InstallOpenRC, InstallBare} {
		if !kind.IsNative() {
			t.Errorf("%v.IsNative() = false, want true (native.go handles it)", kind)
		}
	}
	for _, kind := range []InstallKind{InstallDocker, InstallUnknown} {
		if kind.IsNative() {
			t.Errorf("%v.IsNative() = true, want false", kind)
		}
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
		{InstallOpenRC, "openrc"},
		{InstallBare, "bare"},
		{InstallUnknown, "unknown"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("InstallKind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}
