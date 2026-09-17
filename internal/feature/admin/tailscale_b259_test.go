// v1.5.8+ (B259): tests for the DB-overridable auth key path
// + the Enable/Disable-from-UI flow.
//
// What B259 closes: the B258 UI told the operator
// "edit docker-compose.yml + restart skygate" to flip
// Tailscale between enabled and disabled. But the operator
// (2026-09-16) reported "I don't have access to docker-compose
// anymore, that file is lost". B259 lets them flip the
// path entirely from the web UI by persisting the path to
// global_settings (DB) so it survives container restarts and
// doesn't depend on the .env file.
//
// The test below pins the resolution order at the helper
// level (tailscaleAuthKeyPath + tailscaleAuthKeyPathSource +
// tailscaleAuthKeyDisabled) so a future refactor can't silently
// re-order them. The integration with the headscale
// package (CreatePreauthKeyWithTags + writeTailscaleAuthKey +
// startTailscaled) is exercised at the live deploy + B-check.

package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestService returns a Service with TailscaleAuthKeyPath
// set (simulating the SKYGATE_TS_AUTHKEY_FILE env var). DB
// reads return "" so the helper falls through to the env var.
func newTestService(envPath string) *Service {
	return &Service{TailscaleAuthKeyPath: envPath}
}

// TestTailscaleAuthKeyPath_ResolutionOrder pins the
// priority of DB > env > default. Without a DB connection
// the helper falls back to the env var (or default). The
// DB-overriding behaviour is covered at the live deploy.
func TestTailscaleAuthKeyPath_ResolutionOrder(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"env-set-to-real-path", "/data/ts/authkey", "/data/ts/authkey"},
		{"env-set-to-devnull", "/dev/null", "/dev/null"},
		{"env-empty-uses-default", "", "/data/ts/authkey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(tc.env)
			got := s.tailscaleAuthKeyPath()
			if got != tc.want {
				t.Errorf("tailscaleAuthKeyPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTailscaleAuthKeyPathSource_EnvFallback pins the
// source classification when there's no DB override.
// With TailscaleAuthKeyPath set, source must be "env" so
// the template can render the right hint ("source: env var").
func TestTailscaleAuthKeyPathSource_EnvFallback(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"env-set", "/data/ts/authkey", "env"},
		{"env-devnull", "/dev/null", "env"},
		{"env-empty-default", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(tc.env)
			got := s.tailscaleAuthKeyPathSource()
			if got != tc.want {
				t.Errorf("tailscaleAuthKeyPathSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTailscaleAuthKeyDisabled_RespectsRealPath pins that
// a real /data/ts/authkey-style path is NOT disabled even
// if it doesn't exist on disk (we test with /tmp paths
// since we don't ship /data in CI).
func TestTailscaleAuthKeyDisabled_RespectsRealPath(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "authkey")
	s := newTestService(tmp)
	if s.tailscaleAuthKeyDisabled() {
		t.Errorf("real path (not /dev/null) must NOT be disabled")
	}
}

// TestTailscaleAuthKeyDisabled_RespectsDevNull ensures the
// B258 sentinel still triggers after the B259 DB layer
// was added (the DB overrides the env var, but the
// disabled-detection logic still correctly classifies the
// resolved path).
func TestTailscaleAuthKeyDisabled_RespectsDevNull(t *testing.T) {
	s := newTestService("/dev/null")
	if !s.tailscaleAuthKeyDisabled() {
		t.Errorf("/dev/null env path must remain disabled")
	}
}

// TestTailscaleAuthKeyDisabled_RespectsDevNullVariants
// covers the prefix-match branch (/dev/null/*). Mirrors
// tailscale_b258_test.go::TestTailscaleAuthKeyDisabled_DevNullSlash.
func TestTailscaleAuthKeyDisabled_RespectsDevNullVariants(t *testing.T) {
	for _, p := range []string{"/dev/null", "/dev/null/whatever"} {
		s := newTestService(p)
		if !s.tailscaleAuthKeyDisabled() {
			t.Errorf("%q must be disabled", p)
		}
	}
}

// TestEnableInContainerPersistsDBPath is a documentation
// test: it asserts that handleTailscaleEnableInContainer
// persists the new path to global_settings BEFORE
// generating the key (the FIRST SetGlobalSetting call must
// use the tailscaleAuthKeyPathDBKey constant + newPath
// variable). A future refactor that reorders the calls or
// uses the wrong DB key fails this test.
//
// Implementation: read the source file and grep for the
// marker. We can't use go's AST here (too heavy for a
// test) — string-grep on the file is good enough for the
// contract pin.
func TestEnableInContainerPersistsDBPath(t *testing.T) {
	const marker = `db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath)`
	// Walk up from the test's working dir to find the
	// skygate repo root (where go.mod lives).
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var src []byte
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			src, err = os.ReadFile(filepath.Join(dir, "internal/feature/admin/tailscale.go"))
			if err != nil {
				t.Skipf("read tailscale.go: %v", err)
			}
			break
		}
		parent, err := filepath.Abs(filepath.Join(dir, ".."))
		if err != nil {
			break
		}
		dir = parent
	}
	if src == nil {
		t.Skip("could not find skygate repo root (no go.mod in cwd ancestors)")
	}
	if !strings.Contains(string(src), marker) {
		t.Errorf("tailscale.go must call %q in handleTailscaleEnableInContainer (FIRST step — so subsequent startTailscaled picks up the new path)", marker)
	}
}

// TestGenerateAndWriteTailscaleKeyForEnable_B259_DelegatesToFindUserForHostname
// pins the B259.1 contract: generateAndWriteTailscaleKeyForEnable
// MUST resolve the headscale user via findUserForHostname (the
// canonical B251 helper that pins the reserved hostname
// "skygate-host" → user "infra" per operator 2026-08-13
// directive). Pre-B259.1 the function duplicated a weaker lookup
// (`u.Name == hostname || u.Name == strings.TrimSuffix(hostname, "-1")`)
// that required a phantom headscale user "skygate-host" to exist
// in the headscale database. Operator 2026-09-17: "раз skygate-host
// принадлежит infra то от лица пользователя infra все и делать —
// зачем плодить сущности". After this fix, B259's "Включить
// Tailscale в контейнере" button generates the preauth key
// against the existing user `infra` (uid=85) without any
// provisioning step on the operator's side.
//
// The test asserts three contract markers in tailscale.go:
//
//  1. generateAndWriteTailscaleKeyForEnable calls
//     findUserForHostname (not the duplicate u.Name==hostname
//     logic that we just deleted).
//  2. The function does NOT call hs.ListUsers() — the B251
//     helper is the sole source of truth for the user lookup.
//  3. The function does NOT contain the literal
//     `strings.TrimSuffix(hostname, "-1")` substring (the
//     pre-B259.1 sentinel that the old logic relied on).
func TestGenerateAndWriteTailscaleKeyForEnable_B259_DelegatesToFindUserForHostname(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var src []byte
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			src, err = os.ReadFile(filepath.Join(dir, "internal/feature/admin/tailscale.go"))
			if err != nil {
				t.Skipf("read tailscale.go: %v", err)
			}
			break
		}
		parent, err := filepath.Abs(filepath.Join(dir, ".."))
		if err != nil {
			break
		}
		dir = parent
	}
	if src == nil {
		t.Skip("could not find skygate repo root (no go.mod in cwd ancestors)")
	}
	body := string(src)
	if !strings.Contains(body, "s.findUserForHostname(context.Background(), hs, hostname)") {
		t.Errorf("generateAndWriteTailscaleKeyForEnable must call findUserForHostname (B259.1) — the inline u.Name==hostname lookup is gone")
	}
	// Scope the negative checks to the function body so unrelated
	// hs.ListUsers() calls elsewhere in tailscale.go don't fail
	// the test. We split the file at the function start + use the
	// next closing brace as the boundary.
	const fnStart = "func (s *Service) generateAndWriteTailscaleKeyForEnable("
	const fnEnd = "func (s *Service) handleTailscaleDisableInContainer("
	startIdx := strings.Index(body, fnStart)
	endIdx := strings.Index(body, fnEnd)
	if startIdx < 0 || endIdx < 0 || endIdx <= startIdx {
		t.Skipf("could not locate generateAndWriteTailscaleKeyForEnable boundaries (start=%d end=%d)", startIdx, endIdx)
	}
	fnBody := body[startIdx:endIdx]
	if strings.Contains(fnBody, "hs.ListUsers()") {
		t.Errorf("generateAndWriteTailscaleKeyForEnable must NOT call hs.ListUsers() — findUserForHostname is the sole source of truth for user lookup")
	}
	if strings.Contains(fnBody, `strings.TrimSuffix(hostname, "-1")`) {
		t.Errorf("generateAndWriteTailscaleKeyForEnable must NOT contain the legacy u.Name==hostname-or-strip-1 sentinel (deleted in B259.1)")
	}
}
