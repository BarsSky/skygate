// B272.2 (2026-09-19) — the policy-file permission audit.
//
// Live case (host `aro`, native headscale):
//
//	headscale serves as                 headscale:headscale
//	/etc/headscale/policy.hujson        root:skygate 0660
//	GET /api/v1/policy → 500            reading policy from path
//	                                    "/etc/headscale/policy.hujson": permission denied
//
// So headscale could not read ITS OWN policy: every policy API call failed, no
// `tag:*` could ever be permitted, and the only visible clue was a nested 500
// body inside the tag autoupdater's log. The audit below turns that into a named
// status with ready-to-paste fixes.
package headscale

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestB2722_ModeModelAllows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are what this model reasons about")
	}
	dir := t.TempDir()
	write := func(name string, perm os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), perm); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	cases := []struct {
		name                    string
		perm                    os.FileMode
		hsUser, hsGroup         string
		curUser, curGroup       string
		wantReadableByHeadscale bool
	}{
		// The live failure: only root/skygate can read, headscale cannot.
		{"root:skygate 0660, headscale outside", 0o660, "headscale", "headscale", "skygate", "skygate", false},
		// The fix: skygate writes through the group, headscale owns and reads.
		{"headscale:skygate 0640", 0o640, "headscale", "headscale", "skygate", "skygate", false}, // owner is root in the temp dir
		{"world readable 0644", 0o644, "headscale", "headscale", "skygate", "skygate", true},
		{"owner only 0600", 0o600, "headscale", "headscale", "skygate", "skygate", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := write(strings.ReplaceAll(tc.name, " ", "_"), tc.perm)
			got := modeModelAllows(p, tc.hsUser, tc.hsGroup, tc.curUser, tc.curGroup)
			if got != tc.wantReadableByHeadscale {
				t.Fatalf("modeModelAllows = %v, want %v (mode=%o)", got, tc.wantReadableByHeadscale, tc.perm)
			}
		})
	}
}

// TestB2722_ModeModelAllowsOwnerMatch: when the file IS owned by the headscale
// user, the owner-read bit decides.
func TestB2722_ModeModelAllowsOwnerMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(p, []byte("{}"), 0o400); err != nil {
		t.Fatalf("write: %v", err)
	}
	me, _ := currentServiceIdentity()
	// Pretend the file is owned by the "headscale" identity: the helper must
	// then honour the owner bit rather than the group/other bits.
	if !modeModelAllows(p, me, "", "skygate", "") {
		t.Error("a file owned by the probed user with mode 0400 must count as readable")
	}
	if modeModelAllows(p, "definitely-not-the-owner", "", "skygate", "") {
		t.Error("mode 0400 must NOT count as readable for a different user")
	}
}

func TestB2722_PolicyPermissionFixes(t *testing.T) {
	unreadable := policyPermissionFixes("/etc/headscale/policy.hujson", "headscale", "headscale", "skygate", "skygate", false)
	joined := strings.Join(unreadable, "\n")
	for _, want := range []string{
		"chown headscale:skygate /etc/headscale/policy.hujson",
		"chmod 0640 /etc/headscale/policy.hujson",
		"systemctl restart headscale",
		"sudo -u headscale test -r",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("fix commands missing %q; got:\n%s", want, joined)
		}
	}

	// The second case (headscale can read, skygate cannot write) must NOT
	// suggest chowning the file away from headscale.
	unwritable := strings.Join(policyPermissionFixes("/etc/headscale/policy.hujson", "headscale", "headscale", "skygate", "skygate", true), "\n")
	if strings.Contains(unwritable, "chown") {
		t.Errorf("the write-permission fix must not change ownership; got:\n%s", unwritable)
	}
	if !strings.Contains(unwritable, "chmod 0660") {
		t.Errorf("the write-permission fix must grant group write; got:\n%s", unwritable)
	}
}

// TestB2722_AuditReportsPathAndAccess drives the real audit against a temp file:
// the fields the admin page renders must be populated, and the "not applicable"
// path must be taken when no file-mode policy is configured.
func TestB2722_AuditReportsPathAndAccess(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(policy, []byte(`{"tagOwners":{}}`), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", policy)

	a := AuditHeadscalePolicy()
	if a.Path != policy {
		t.Fatalf("Path = %q, want %q", a.Path, policy)
	}
	if !a.CurrentUserReadable {
		t.Error("CurrentUserReadable must be true for a file we just wrote")
	}
	// The kernel answers the write question on Unix; on Windows the bit model
	// is not meaningful, so only assert the Unix behaviour.
	if runtime.GOOS != "windows" && !a.CurrentUserWritable {
		t.Error("CurrentUserWritable must be true for a file the test process just wrote")
	}
	if a.Detail == "" {
		t.Error("Detail must always explain the verdict")
	}
	if a.ProbeMethod == "" {
		t.Error("ProbeMethod must say whether a real probe or the mode model was used")
	}
}

// TestB2722_NoFileModePolicyIsNotApplicable: with nothing configured the audit
// must not invent a problem (a database-mode install has no policy file).
func TestB2722_NoFileModePolicyIsNotApplicable(t *testing.T) {
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", "")
	t.Setenv("SKYGATE_HEADSCALE_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	a := AuditHeadscalePolicy()
	if a.Status != PolicyAuditNotApplicable {
		t.Fatalf("Status = %q, want not_applicable", a.Status)
	}
	if !a.OK() {
		t.Error("not_applicable must count as OK (nothing to fix)")
	}
	if len(a.Fixes) != 0 {
		t.Errorf("Fixes = %v, want none for not_applicable", a.Fixes)
	}
}

// TestB2722_MissingFileIsReported: a configured path with no file is a real
// problem (headscale answers 500 for every policy call).
func TestB2722_MissingFileIsReported(t *testing.T) {
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", filepath.Join(t.TempDir(), "nope.hujson"))
	a := AuditHeadscalePolicy()
	if a.Status != PolicyAuditMissing {
		t.Fatalf("Status = %q, want missing", a.Status)
	}
	if a.OK() {
		t.Error("a missing policy file must not be reported as OK")
	}
	if len(a.Fixes) == 0 {
		t.Error("a missing policy file must come with fix commands")
	}
}
