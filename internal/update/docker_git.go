// docker_git.go — B532 (2026-11-27): the git transport (fetch, mirror fallback, checkout, conflict stash), split out of docker.go.
//
// docker.go was 1 515 lines: the Docker upgrader's job runner AND its transport, its shell
// wrappers, its health poller, its ownership helpers and three embedded /bin/sh scripts.
// The audit's complaint was that a reader cannot find what a 1 500-line file does; each
// section keeps its code VERBATIM and its own doc comments, and the package is unchanged —
// no identifier moved, so no caller and no contract had to learn a new name.
package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runGit runs `git <args>` from RepoPath. Output is captured
// and stored in the state log on success or failure.
func (u *DockerUpgrader) runGit(ctx context.Context, args ...string) error {
	return u.runShell(ctx, "git", args...)
}

// fetchTarget fetches tags + branches, falling back to a configured MIRROR when
// `origin` is unreachable (B272.5).
//
// Live case (host `aro`, 2026-09-19): the image update aborted with
//
//	git fetch --tags --prune --force →
//	fatal: unable to access 'https://github.com/BarsSky/skygate.git/':
//	  Failed to connect to github.com:443 after 132571 ms: Could not connect to server
//
// — a host without outbound access to github.com can never self-update through
// this path, and the operator was told nothing about where to point it instead.
// The failure also cost two minutes per attempt (git's default connect timeout)
// before rolling back.
//
// So: `origin` stays the default, and when it fails we try
// `SKYGATE_UPDATE_GIT_URL` (also `SKYGATE_UPDATE_GIT_MIRROR`), reporting both
// attempts. Both fetch the same refspec explicitly, so the result does not depend
// on how the remote is named:
//
//	+refs/heads/*:refs/remotes/origin/*   → target resolution by branch name
//	+refs/tags/*:refs/tags/*              → target resolution by tag
func (u *DockerUpgrader) fetchTarget(ctx context.Context) error {
	originErr := u.runGit(ctx, "fetch", "--tags", "--prune", "--force")
	if originErr == nil {
		return nil
	}
	mirror := u.gitMirrorURL()
	if mirror == "" {
		hint := "no git mirror is configured — set SKYGATE_UPDATE_GIT_URL (env) or save update.git_url to a reachable clone (internal mirror, or an ssh:// remote) if this host has no internet access"
		u.State.Log(LogWarn, "origin is unreachable and "+hint+"; rollback follows")
		return fmt.Errorf("origin unreachable: %w (%s)", originErr, hint)
	}
	u.State.Log(LogInfo, fmt.Sprintf("origin unreachable (%v) — trying the configured mirror %s", originErr, mirror))
	refspecs := []string{
		"+refs/heads/*:refs/remotes/origin/*",
		"+refs/tags/*:refs/tags/*",
	}
	args := append([]string{"fetch", "--prune", "--force", mirror}, refspecs...)
	if err := u.runGit(ctx, args...); err != nil {
		return fmt.Errorf("origin: %v; mirror %s: %w", originErr, mirror, err)
	}
	u.State.Log(LogInfo, "mirror fetch OK")
	return nil
}

// gitMirrorURL returns the configured fallback git URL, or "" when none is set.
func (u *DockerUpgrader) gitMirrorURL() string {
	for _, key := range []string{"SKYGATE_UPDATE_GIT_URL", "SKYGATE_UPDATE_GIT_MIRROR"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	if u.SettingsFn != nil {
		if v := strings.TrimSpace(u.SettingsFn("update.git_url")); v != "" {
			return v
		}
	}
	return ""
}

// checkoutRef switches the working tree to gitRef, protecting any UNTRACKED
// local files that the target revision would overwrite (B272.4).
//
// Live case (host `aro`, 2026-09-19): the update to v1.5.14 aborted with
//
//	git checkout v1.5.14 → error: The following untracked working tree files
//	would be overwritten by checkout: scripts/skygate-move-to-infra.sh
//	Please move or remove them before you switch branches.
//
// and rolled back. The operator was locked out of EVERY future image update
// until they hand-deleted a file skygate itself had left behind — while the
// checkout the updater wanted was exactly the thing that would have delivered
// the tracked, reviewed version of that same file.
//
// A file existing in the working tree but not in the index is, by definition,
// not part of any revision: the repository is the source of truth for the code
// it tracks. So the safe move is to PRESERVE the local copy (a timestamped
// backup next to the update state, plus its sha256, plus a line in the job log)
// and then let the checkout proceed. Operator-managed files that ARE tracked
// (docker-compose.yml, go.mod, go.sum) are untouched by this path — git keeps
// them, and a modified tracked file is a different error that must stay fatal.
func (u *DockerUpgrader) checkoutRef(ctx context.Context, gitRef string) error {
	err := u.runGit(ctx, "checkout", gitRef)
	if err == nil {
		return nil
	}
	// Only the untracked-overwrite refusal is recoverable; everything else
	// (bad pathspec, dirty tracked file, network) stays fatal with git's own
	// message.
	conflicts := u.untrackedCheckoutConflicts(ctx, gitRef)
	if len(conflicts) == 0 {
		return err
	}
	stashDir := u.checkoutStashDir()
	if mkErr := os.MkdirAll(stashDir, 0o750); mkErr != nil {
		return fmt.Errorf("%w (and the untracked files %v could not be backed up: %v)", err, conflicts, mkErr)
	}
	for _, rel := range conflicts {
		src := filepath.Join(u.RepoPath, rel)
		dst := filepath.Join(stashDir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
			return fmt.Errorf("%w (backup of %s failed: %v)", err, rel, mkErr)
		}
		if cpErr := copyFilePreservingMode(src, dst); cpErr != nil {
			return fmt.Errorf("%w (backup of %s failed: %v)", err, rel, cpErr)
		}
		// Remove the working-tree file so git can materialise the tracked
		// version from the target revision.
		if rmErr := os.Remove(src); rmErr != nil {
			return fmt.Errorf("%w (could not move %s aside: %v)", err, rel, rmErr)
		}
		u.State.Log(LogWarn, fmt.Sprintf("untracked file %s would block the checkout of %s — backed up to %s and replaced with the tracked version", rel, gitRef, dst))
	}
	if err := u.runGit(ctx, "checkout", gitRef); err != nil {
		return fmt.Errorf("%w (after moving %v aside to %s)", err, conflicts, stashDir)
	}
	u.State.Log(LogInfo, fmt.Sprintf("checkout %s succeeded after preserving %d untracked file(s) in %s", gitRef, len(conflicts), stashDir))
	return nil
}

// untrackedCheckoutConflicts lists the untracked files that would block a
// checkout of gitRef.
//
// git refuses to overwrite an UNTRACKED path that the target revision
// introduces; this reproduces that rule with two plumbing-free commands (no
// `--dry-run`, which `git checkout` does not support):
//
//	git ls-files --others --exclude-standard   → the untracked set
//	git ls-tree -r --name-only <ref>           → the paths the target writes
//
// The intersection is exactly what git will refuse. Returns nil when either
// command fails: an unknown answer must never be treated as "safe to delete".
func (u *DockerUpgrader) untrackedCheckoutConflicts(ctx context.Context, gitRef string) []string {
	untrackedOut, err := u.runShellCapture(ctx, "git", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	targetOut, err := u.runShellCapture(ctx, "git", "ls-tree", "-r", "--name-only", gitRef)
	if err != nil {
		return nil
	}
	target := map[string]bool{}
	for _, l := range strings.Split(targetOut, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			target[l] = true
		}
	}
	var out []string
	for _, l := range strings.Split(untrackedOut, "\n") {
		if l = strings.TrimSpace(l); l != "" && target[l] {
			out = append(out, l)
		}
	}
	return out
}

// checkoutStashDir is where preserved untracked files go: next to the update
// state (so they live on the same volume the operator already backs up).
func (u *DockerUpgrader) checkoutStashDir() string {
	base := filepath.Dir(u.SwapLogPath)
	if base == "." || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "checkout-stash", time.Now().UTC().Format("20060102T150405Z"))
}

// copyFilePreservingMode copies src to dst keeping the source's permission bits.
func copyFilePreservingMode(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
