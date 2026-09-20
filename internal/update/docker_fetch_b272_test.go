// B272.5 (2026-09-19) — a host without outbound access to github.com must still
// be able to self-update.
//
// Live case (host `aro`): the image update aborted with
//
//	git fetch --tags --prune --force →
//	fatal: unable to access 'https://github.com/BarsSky/skygate.git/':
//	  Failed to connect to github.com:443 after 132571 ms: Could not connect to server
//
// and rolled back. Nothing in the product told the operator that the fetch source
// is configurable, and the attempt burned two minutes (git's connect timeout)
// before failing.
//
// These tests drive the REAL git binary against local repositories, so the
// fallback refspecs are pinned end to end: after the mirror fetch, the target tag
// resolves exactly as it would have from origin.
package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mirrorFixture builds: an "upstream" repo with a committed file and the tag
// v9.9.9, its bare clone (the mirror), and a working repo whose origin points at
// a path that does not exist.
func mirrorFixture(t *testing.T, tag string) (work, mirror, upstream string) {
	t.Helper()
	upstream = t.TempDir()
	gitRun(t, upstream, "init", "-q")
	gitRun(t, upstream, "config", "user.email", "t@e")
	gitRun(t, upstream, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(upstream, "app.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-q", "-m", "release")
	gitRun(t, upstream, "tag", tag)

	mirror = filepath.Join(t.TempDir(), "mirror.git")
	gitRun(t, upstream, "clone", "-q", "--bare", upstream, mirror)

	work = t.TempDir()
	gitRun(t, work, "init", "-q")
	gitRun(t, work, "config", "user.email", "t@e")
	gitRun(t, work, "config", "user.name", "t")
	gitRun(t, work, "remote", "add", "origin", filepath.Join(t.TempDir(), "does-not-exist.git"))
	return work, mirror, upstream
}

func TestB2725_FetchFallsBackToMirror(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	work, mirror, _ := mirrorFixture(t, "v9.9.9")
	u := NewDockerUpgrader(work, NewStateStore(filepath.Join(t.TempDir(), "state.json")), "v1.0.0")

	t.Setenv("SKYGATE_UPDATE_GIT_URL", mirror)
	t.Setenv("SKYGATE_UPDATE_GIT_MIRROR", "")

	if err := u.fetchTarget(context.Background()); err != nil {
		t.Fatalf("fetchTarget: %v", err)
	}
	// The tag must exist locally with the upstream sha — that is what the
	// checkout phase resolves.
	local := strings.TrimSpace(gitRun(t, work, "rev-parse", "refs/tags/v9.9.9"))
	if local == "" {
		t.Fatal("the mirror fetch did not bring the tag")
	}
	// And the refspec must have populated refs/remotes/origin/*, so a branch
	// target (e.g. "main") resolves too.
	branches := gitRun(t, work, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/")
	if !strings.Contains(branches, "refs/remotes/origin/") {
		t.Fatalf("the mirror fetch did not populate remote-tracking branches:\n%s", branches)
	}
}

func TestB2725_NoMirrorConfiguredIsReportedAndFails(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	work, _, _ := mirrorFixture(t, "v9.9.9")
	u := NewDockerUpgrader(work, NewStateStore(filepath.Join(t.TempDir(), "state.json")), "v1.0.0")
	u.SwapLogPath = filepath.Join(t.TempDir(), "swap.log")

	t.Setenv("SKYGATE_UPDATE_GIT_URL", "")
	t.Setenv("SKYGATE_UPDATE_GIT_MIRROR", "")

	err := u.fetchTarget(context.Background())
	if err == nil {
		t.Fatal("an unreachable origin with no mirror must fail (the update then rolls back)")
	}
	// The error must name the knob that fixes it — the whole point of the
	// change: the operator learns where to point the updater.
	if !strings.Contains(err.Error(), "SKYGATE_UPDATE_GIT_URL") {
		t.Errorf("error = %v, want it to name SKYGATE_UPDATE_GIT_URL", err)
	}
	if !strings.Contains(err.Error(), "origin unreachable") {
		t.Errorf("error = %v, want it to say the origin was unreachable", err)
	}
}

func TestB2725_MirrorURLPrecedence(t *testing.T) {
	u := NewDockerUpgrader(t.TempDir(), NewStateStore(filepath.Join(t.TempDir(), "state.json")), "v1")

	t.Setenv("SKYGATE_UPDATE_GIT_URL", "")
	t.Setenv("SKYGATE_UPDATE_GIT_MIRROR", "")
	if got := u.gitMirrorURL(); got != "" {
		t.Errorf("gitMirrorURL = %q, want empty when nothing is configured", got)
	}

	t.Setenv("SKYGATE_UPDATE_GIT_MIRROR", "ssh://git@mirror.local/skygate.git")
	if got := u.gitMirrorURL(); got != "ssh://git@mirror.local/skygate.git" {
		t.Errorf("gitMirrorURL = %q, want the mirror env value", got)
	}

	// SKYGATE_UPDATE_GIT_URL wins (the documented knob).
	t.Setenv("SKYGATE_UPDATE_GIT_URL", "http://mirror.local/skygate.git")
	if got := u.gitMirrorURL(); got != "http://mirror.local/skygate.git" {
		t.Errorf("gitMirrorURL = %q, want SKYGATE_UPDATE_GIT_URL to win", got)
	}

	// The DB value is the fallback when no env var is set (so an operator can
	// save it from /admin/update without restarting the service).
	t.Setenv("SKYGATE_UPDATE_GIT_URL", "")
	t.Setenv("SKYGATE_UPDATE_GIT_MIRROR", "")
	u.SettingsFn = func(key string) string {
		if key == "update.git_url" {
			return "http://db-mirror.local/skygate.git"
		}
		return ""
	}
	if got := u.gitMirrorURL(); got != "http://db-mirror.local/skygate.git" {
		t.Errorf("gitMirrorURL = %q, want the global_settings value", got)
	}
}
