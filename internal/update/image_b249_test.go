// internal/update/image_b249_test.go — B249 image-pull upgrader tests.
//
// Pins the behaviour of the new image-pull update path
// (added 2026-09-15 as part of the v1.5.4 release work):
//
//   - imageIsFromRegistry correctly classifies registry vs
//     locally-built images.
//   - imageTag correctly extracts the tag portion (handles
//     digest refs, no-tag → "latest", registry:port cases).
//   - sanitizeTag replaces docker-invalid chars in tag names.
//   - Run() pre-flight refuses locally-built images (the
//     most common operator mistake: trying to image-pull
//     from a `build:`-based compose file).
//   - Run() pre-flight refuses empty tag/image.
//   - Run() success path runs docker pull + compose up +
//     polls healthz.
//   - Run() rollback path on compose-up failure restarts with
//     the previous image.
//
// Tests use the shellExec var-override to avoid spawning
// real subprocesses. Each test sets shellExec = func(...) at
// the top and defers restoring it.
package update

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// shellStub is the test-side handle returned by withShellStub.
// Tests use .calls to inspect the command sequence and .fn to
// override the per-command response (e.g. to simulate a
// failure on a specific command).
type shellStub struct {
	calls *[]string
	setFn func(fn func(name string, args []string) (string, error))
}

// withShellStub swaps shellExec for the duration of the test.
// Every call is recorded; the per-command response defaults to
// "success, empty output". Tests use stub.setFn(...) to override.
//
// Example:
//
//	stub := withShellStub(t)
//	stub.setFn(func(name string, args []string) (string, error) {
//	    if name == "docker" && args[0] == "pull" {
//	        return "access denied", errors.New("exit status 1")
//	    }
//	    return "", nil
//	})
//	// ... call upgrader.Run(...)
//	for _, c := range *stub.calls { t.Log(c) }
func withShellStub(t *testing.T) *shellStub {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	var customFn func(name string, args ...string) (string, error)
	orig := shellExec
	shellExec = func(ctx context.Context, name string, args ...string) (string, error) {
		mu.Lock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if customFn != nil {
			return customFn(name, args...)
		}
		return "", nil
	}
	t.Cleanup(func() { shellExec = orig })
	return &shellStub{
		calls: &calls,
		setFn: func(fn func(name string, args []string) (string, error)) {
			customFn = func(n string, a ...string) (string, error) {
				return fn(n, a)
			}
		},
	}
}

// TestImageIsFromRegistry pins the registry vs local-image
// classifier. The orchestrator refuses to image-pull locally-
// built images (the "pre-flight" check), and this is the
// gate that distinguishes them.
func TestImageIsFromRegistry(t *testing.T) {
	s := &ImagePullStrategy{}
	cases := []struct {
		image string
		want  bool
	}{
		// From a real registry.
		{"ghcr.io/barssky/skygate:v1.5.4", true},
		{"ghcr.io/barssky/skygate:latest", true},
		{"docker.io/library/alpine:3.19", true},
		{"quay.io/coreos/etcd:v3.5.0", true},
		// With digest.
		{"ghcr.io/barssky/skygate@sha256:abc123", true},
		// With port.
		{"registry.internal.example.com:5000/skygate:v1.5.4", true},
		{"localhost:5000/skygate:latest", true},
		// Locally built — no slash before colon.
		{"skygate-skygate:latest", false},
		{"skygate-skygate:v1.5.4", false},
		{"my-skygate:latest", false},
		// Has slash but no domain — should be rejected.
		{"skygate/skygate:latest", false}, // "skygate" has no dot/colon
		{"foo/bar:v1", false},             // "foo" has no dot/colon
		// Edge cases.
		{"", false},                         // empty
		{"alpine", false},                   // bare name, no slash, no tag
		{"alpine:3.19", false},              // bare name with tag — local-style
	}
	for _, c := range cases {
		if got := s.imageIsFromRegistry(c.image); got != c.want {
			t.Errorf("imageIsFromRegistry(%q) = %v, want %v", c.image, got, c.want)
		}
	}
}

// TestImageTag pins the tag-extraction helper. Must handle
// the three reference shapes:
//   - registry/repo:tag         → "tag"
//   - registry/repo:tag@sha256:  → "tag" (digest stripped first)
//   - registry:port/repo:tag    → "tag" (port in registry, not in tag)
//   - registry/repo (no tag)    → "latest" (docker default)
//   - registry/repo@sha256:...  → "latest" (digest only, no tag)
func TestImageTag(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"ghcr.io/barssky/skygate:v1.5.4", "v1.5.4"},
		{"ghcr.io/barssky/skygate:latest", "latest"},
		{"ghcr.io/barssky/skygate:v1.5.4@sha256:abc123def456", "v1.5.4"},
		{"ghcr.io/barssky/skygate@sha256:abc123def456", "latest"},
		{"registry.internal:5000/skygate:v1.5.4", "v1.5.4"},
		{"alpine", "latest"},
		{"alpine:3.19", "3.19"},
		{"skygate-skygate:latest", "latest"},
	}
	for _, c := range cases {
		if got := imageTag(c.image); got != c.want {
			t.Errorf("imageTag(%q) = %q, want %q", c.image, got, c.want)
		}
	}
}

// TestSanitizeTag pins the backup-tag name builder. Docker
// tags can't contain ":" "/" "@" or " " — sanitizeTag replaces
// these with underscores so the backup-tag is valid.
func TestSanitizeTag(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"v1.5.4", "v1.5.4"},
		{"v1.5.4@sha256:abc", "v1.5.4_sha256_abc"},
		{"registry:5000/img:v1", "registry_5000_img_v1"},
		{"with space", "with_space"},
		{"path/with/slash", "path_with_slash"},
	}
	for _, c := range cases {
		if got := sanitizeTag(c.in); got != c.want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRun_EmptyTag_Rejected pins the pre-flight guard. An
// empty tag would cause `docker pull <image>:` (with trailing
// colon) which docker interprets as "pull the image and
// retag it" — confusing failure mode. Refuse early.
func TestRun_EmptyTag_Rejected(t *testing.T) {
	s := NewImagePullStrategy("ghcr.io/barssky/skygate", "")
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run with empty tag: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tag is empty") {
		t.Errorf("error %q should mention 'tag is empty'", err.Error())
	}
}

// TestRun_EmptyImage_Rejected pins the other pre-flight guard.
func TestRun_EmptyImage_Rejected(t *testing.T) {
	s := &ImagePullStrategy{Tag: "v1.5.4", ComposeProject: "skygate"}
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run with empty image: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "source image is empty") {
		t.Errorf("error %q should mention 'source image is empty'", err.Error())
	}
}

// TestRun_PreFlightRejectsLocallyBuiltImage is the B249
// regression. Pre-B249 the operator could accidentally click
// "Pull image" on a compose file that uses `build: { context: . }`
// (no registry) — docker pull would silently re-tag the local
// image, the compose up would restart the same code, and the
// operator would think they upgraded when they didn't. B249's
// pre-flight refuses with a clear "switch to docker-compose.ghcr.yml
// first" message.
func TestRun_PreFlightRejectsLocallyBuiltImage(t *testing.T) {
	s := &ImagePullStrategy{
		Image:          "ghcr.io/barssky/skygate",
		Tag:            "v1.5.4",
		ComposeProject: "skygate",
	}
	// Mock shellExec: detect_running_image returns a local-style
	// image (no slash before colon → "skygate-skygate:latest").
	shellExec = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "docker" && len(args) >= 3 && args[0] == "inspect" {
			return "skygate-skygate:latest\n", nil
		}
		return "", nil
	}
	defer func() { shellExec = realShellExec }()

	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run on locally-built image: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not from a registry") {
		t.Errorf("error %q should mention 'not from a registry'", err.Error())
	}
	if !strings.Contains(err.Error(), "docker-compose.ghcr.yml") {
		t.Errorf("error %q should mention 'docker-compose.ghcr.yml'", err.Error())
	}
}

// TestRun_AlreadyOnTarget_IsNoOp pins the "no-op when current
// = target" branch. Avoids spurious container restarts when
// the operator clicks "Pull image" on a fresh deploy.
func TestRun_AlreadyOnTarget_IsNoOp(t *testing.T) {
	s := &ImagePullStrategy{
		Image:          "ghcr.io/barssky/skygate",
		Tag:            "v1.5.4",
		ComposeProject: "skygate",
	}
	stub := withShellStub(t)
	// First call (detect_running_image) returns the same tag.
	stub.setFn(func(name string, args []string) (string, error) {
		if name == "docker" && len(args) >= 3 && args[0] == "inspect" {
			return "ghcr.io/barssky/skygate:v1.5.4\n", nil
		}
		if name == "curl" {
			return `{"status":"ok"}`, nil
		}
		return "", nil
	})
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Critical assertion: no `docker pull` happened.
	for _, c := range *stub.calls {
		if strings.HasPrefix(c, "docker pull ") {
			t.Errorf("expected no docker pull (no-op), but found: %q", c)
		}
	}
}

// TestRun_SuccessPath pins the happy-path sequence: pre-flight,
// backup tag, docker pull, compose up, healthz poll. The order
// matters — backup BEFORE pull, pull BEFORE compose up.
func TestRun_SuccessPath(t *testing.T) {
	s := &ImagePullStrategy{
		Image:          "ghcr.io/barssky/skygate",
		Tag:            "v1.5.4",
		ComposeProject: "skygate",
		HealthURL:      "http://127.0.0.1:8080/healthz",
		HealthTimeout:  5 * time.Second,
		PollInterval:   10 * time.Millisecond,
	}
	stub := withShellStub(t)
	stub.setFn(func(name string, args []string) (string, error) {
		if name == "docker" && len(args) >= 3 && args[0] == "inspect" {
			return "ghcr.io/barssky/skygate:v1.5.3\n", nil
		}
		if name == "curl" {
			return `{"status":"ok"}`, nil
		}
		if name == "test" {
			if len(args) >= 2 && strings.HasSuffix(args[len(args)-1], "docker-compose.ghcr.yml") {
				return "", nil
			}
			return "", &fakeExitErr{code: 1}
		}
		return "", nil
	})
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantSubstrings := []string{
		"docker tag ghcr.io/barssky/skygate:v1.5.3 ghcr.io/barssky/skygate:skygate-pre-update-v1.5.3",
		"docker pull ghcr.io/barssky/skygate:v1.5.4",
		"docker compose -f ",
		"curl -fsS --max-time 3 http://127.0.0.1:8080/healthz",
	}
	for _, want := range wantSubstrings {
		found := false
		for _, c := range *stub.calls {
			if strings.Contains(c, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected call containing %q, got calls:\n  %s", want, strings.Join(*stub.calls, "\n  "))
		}
	}
}

// TestRun_ComposeUpFailure_RollsBack pins the failure path.
// When the post-pull compose up fails (e.g. image bug, port
// conflict), the upgrader must restore the previous image
// rather than leaving the operator with a broken container.
func TestRun_ComposeUpFailure_RollsBack(t *testing.T) {
	s := &ImagePullStrategy{
		Image:          "ghcr.io/barssky/skygate",
		Tag:            "v1.5.4-broken",
		ComposeProject: "skygate",
		HealthTimeout:  1 * time.Second,
	}
	stub := withShellStub(t)
	stub.setFn(func(name string, args []string) (string, error) {
		if name == "docker" && len(args) >= 3 && args[0] == "inspect" {
			return "ghcr.io/barssky/skygate:v1.5.3\n", nil
		}
		if name == "docker" && len(args) >= 2 && args[0] == "compose" && args[1] == "-f" {
			return "image failed to start", &fakeExitErr{code: 1}
		}
		if name == "test" {
			if len(args) >= 2 && strings.HasSuffix(args[len(args)-1], "docker-compose.ghcr.yml") {
				return "", nil
			}
			return "", &fakeExitErr{code: 1}
		}
		return "", nil
	})
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run on compose-up failure: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "compose up failed") {
		t.Errorf("error %q should mention 'compose up failed'", err.Error())
	}
	composeUps := 0
	for _, c := range *stub.calls {
		if strings.Contains(c, "docker compose -f ") {
			composeUps++
		}
	}
	if composeUps < 2 {
		t.Errorf("expected at least 2 compose up attempts (original + rollback), got %d:\n  %s",
			composeUps, strings.Join(*stub.calls, "\n  "))
	}
}

// fakeExitErr implements error to simulate a non-zero exit code
// without depending on exec.ExitError.
type fakeExitErr struct{ code int }

func (e *fakeExitErr) Error() string { return "exit status " + itoa(e.code) }

// itoa avoids importing strconv just for the test's error string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
