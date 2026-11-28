// docker_labels.go — B532 (2026-11-27): the build labels, the host-owner helpers and output truncation, split out of docker.go.
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
	"os"
	"regexp"
	"strings"
)

// GitRefForBuildLabel converts a build label / display
// target into a valid git ref that can be passed to
// `git checkout` (or any other `git <ref>` command).
//
// The build label has THREE historical shapes, all of which
// must round-trip through this helper without breaking:
//
//  1. Tagged release:    "v1.5.0"
//     → "v1.5.0"          (valid tag, no transformation)
//
//  2. Describe-style:    "v1.5.0-3-gabc1234"  (git describe
//     on a commit N ahead of v1.5.0)
//     → "v1.5.0-3-gabc1234"  (valid pseudo-tag, no transform)
//
//  3. Describe + dup:    "v1.5.0-3-gabc1234+abc1234"
//     → "v1.5.0-3-gabc1234"  (strip the "+<commit>" suffix
//     added by main.go's BuildVersion
//     concatenation when `version`
//     already contains "-g<hex>")
//
//  4. Plain tag + commit: "v1.5.0+abc1234"
//     → "v1.5.0"            (strip the "+<commit>")
//
//  5. Untagged build (operator live, 2026-09-04):
//     "e2d0b9e+e2d0b9e"  — no tag ancestor near the commit,
//     `git describe --tags --always` returns just the short
//     SHA, so `version = e2d0b9e` and the main.go concat
//     produces "<sha>+<sha>". The `+` is not a valid
//     character in a git pathspec, so `git checkout e2d0b9e
//     +e2d0b9e` errors with "pathspec did not match any
//     file(s) known to git".
//     → "e2d0b9e"           (strip the "+<commit>" and the
//     stale "v" prefix that
//     normalizeUpdateTarget may have
//     added — see below)
//
//  6. After normalizeUpdateTarget prepends "v":
//     "ve2d0b9e+e2d0b9e"   — the normalizeUpdateTarget helper
//     adds a "v" prefix to bare inputs that don't look like
//     a ref (no "v" / "skygate-" / "main" / "HEAD" prefix).
//     "e2d0b9e+e2d0b9e" doesn't start with "v", so
//     normalize makes it "ve2d0b9e+e2d0b9e" — a string that
//     can never be a valid git ref (the "v" is just a display
//     convention).
//     → "e2d0b9e"           (the leading "v" is stripped only
//     when the remainder is a pure hex
//     SHA, NOT a semver like "1.5.0")
//
// The "+<commit>" strip is the critical part — `+` is the
// one character that always makes a git pathspec invalid,
// and BuildVersion's concatenation is the only path that
// produces it. The leading-"v" strip is a defense-in-depth
// for the normalizeUpdateTarget + BuildVersion interaction
// (the form posts `target=ve2d0b9e+e2d0b9e` because the
// "Push update" form has no `target` field and falls back
// to `target = s.BuildVersion` after normalize).
//
// The isAllHex check is the key: "v1.5.0" → "1.5.0" is
// NOT all-hex (dot is not hex), so we keep the "v" and
// "v1.5.0" passes through unchanged. "ve2d0b9e" →
// "e2d0b9e" IS all-hex, so we strip the "v". This means
// the helper is safe to call on any display string —
// legitimate semver tags are untouched, and the two broken
// patterns (5 + 6) get cleanly reduced to a plain SHA.
func GitRefForBuildLabel(s string) string {
	if s == "" {
		return s
	}
	// Step 1: strip the "+<commit>" suffix. This is the
	// only transformation that's ALWAYS required (any
	// remaining "+" makes the ref invalid as a pathspec).
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	// Step 2: defense-in-depth — if what remains looks
	// like a "v<hex>" (a short commit with a stray "v"
	// prefix added by normalizeUpdateTarget), strip the
	// "v" so we end up with a plain SHA that git
	// checkout can resolve. We check the remainder is
	// pure hex (no dots, no dashes) so legitimate
	// semver tags like "v1.5.0" are NOT affected.
	if len(s) > 1 && s[0] == 'v' && isAllHex(s[1:]) {
		s = s[1:]
	}
	return s
}

// isAllHex is defined in checker.go (same package) and
// reused here. The signature is identical — both files
// can call it freely.

// shortSHA returns the first 8 hex chars of a build label
// (e.g. "v0.28.6-21-ge3ce6f0+e3ce6f0" → "e3ce6f0"). Used
// for the backup tag name; the full label would also work
// but 8 chars is enough for `git show` to disambiguate.
func shortSHA(buildLabel string) string {
	// Format: vX.Y.Z-N-g<short>+<short> or vX.Y.Z+<short>
	// We want the LAST "+" segment (the build hash, not
	// the tag hash). For our case both are the same SHA
	// (the +commit part from -ldflags), so either works.
	idx := strings.LastIndex(buildLabel, "+")
	if idx < 0 || idx+1 >= len(buildLabel) {
		// Fallback: hash the whole label
		return "unknown"
	}
	hash := buildLabel[idx+1:]
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return hash
}

// ownerPattern validates a "uid:gid" string. We accept
// only digits + colon, e.g. "1000:1000". Anything else
// (typos, "admin:admin" by name, etc.) is rejected
// so a misconfigured SKYGATE_HOST_OWNER env doesn't cause
// chown to fail with a confusing error.
var ownerPattern = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// detectHostOwner captures the host-side uid:gid for the
// source tree. Used by chownToHostOwner to restore file
// ownership after the orchestrator's git mutations.
//
// Resolution order:
//  1. Already-cached value (idempotent — first call wins).
//  2. SKYGATE_HOST_OWNER env var (e.g. "1000:1000"). This
//     is the operator's escape hatch for non-standard
//     layouts (different UID for admin, rootless
//     Docker with a different host user, etc.).
//  3. `stat -c '%u:%g' <RepoPath>/.git/HEAD` — that file
//     was created by the host's `git init` / `git clone`
//     so its owner is the host owner. (We capture this
//     BEFORE any git mutation; after that .git/HEAD is
//     also root:root, so the call site must invoke
//     detectHostOwner at the top of Run, before any
//     runGit call.)
//  4. Fallback "1000:1000" — the standard Ubuntu first
//     user UID, which is admin on the operator's VM.
//     For the 99% case this is correct; operators with a
//     non-standard layout use the env override.
func (u *DockerUpgrader) detectHostOwner(ctx context.Context) (string, error) {
	if u.hostOwner != "" {
		return u.hostOwner, nil
	}
	if v := os.Getenv("SKYGATE_HOST_OWNER"); v != "" {
		if !ownerPattern.MatchString(v) {
			return "", fmt.Errorf("SKYGATE_HOST_OWNER=%q is not a valid uid:gid", v)
		}
		u.hostOwner = v
		return u.hostOwner, nil
	}
	// Auto-detect via stat. The marker file is .git/HEAD
	// — present in any working git checkout, owned by the
	// host user (not by us, even inside the container,
	// UNTIL the first git mutation runs).
	marker := u.RepoPath + "/.git/HEAD"
	out, err := u.runShellCapture(ctx, "stat", "-c", "%u:%g", marker)
	if err == nil {
		owner := strings.TrimSpace(out)
		if ownerPattern.MatchString(owner) {
			u.hostOwner = owner
			return u.hostOwner, nil
		}
	}
	// Fallback: standard first user.
	u.hostOwner = "1000:1000"
	return u.hostOwner, nil
}

// chownToHostOwner runs `chown -R <host-owner> <paths...>`.
// Used after every git mutation phase to keep the bind-
// mount's file ownership in sync with the host user —
// otherwise the operator's next `git pull` from the host
// shell fails with "Permission denied".
//
// Why no sudo: inside the skygate container we run as
// root (the entrypoint does `chmod 777 /app`), so chown
// works directly. On a bare/systemd host the orchestrator
// runs as root too (the systemd unit's User=root or the
// admin user; either way, chown to a different uid
// requires CAP_FOWNER which root has). The previous
// version of this code prefixed `sudo`, which broke in
// the Alpine container (no sudo binary).
func (u *DockerUpgrader) chownToHostOwner(ctx context.Context, paths ...string) error {
	owner, err := u.detectHostOwner(ctx)
	if err != nil {
		return err
	}
	args := []string{"-R", owner}
	args = append(args, paths...)
	return u.runShell(ctx, "chown", args...)
}

// truncateOutput limits a captured stdout/stderr to max
// chars for the log buffer. Full output stays in the
// shell history on the host; we don't echo it into the
// status file (which the page renders inline).
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
