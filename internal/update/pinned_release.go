// B346 (2026-10-04) — the release every instance is pinned to.
//
// Why this exists
// ---------------
// "Update to the latest release" is not a version policy: GitHub's latest
// moves, so the primary, a cluster standby provisioned a week later and an
// agent host installed from a copy-pasted command can each end up on a
// different release. Live (2026-10-04) the operator's primary answered
// `/healthz` with `"build":"v1.5.94+4b2186b"` while its WORKING TREE carried
// 21 commits that no tag described: every instance was "up to date" and no
// two of them were the same code.
//
// A pinned release is one tag, stored in `global_settings` and read by every
// path that chooses a target:
//
//   - the /admin/update page's target (and therefore "Update now"),
//   - the B130 scheduled auto-update (which otherwise follows GitHub latest),
//   - the B342 cluster-onboarding install block, so a joining host is
//     provisioned with the SAME tag as the primary instead of whatever the
//     primary happens to be built from.
//
// Empty value = follow the latest release (pre-B346 behaviour), so an
// operator who never opens the section is unaffected.
package update

import (
	"database/sql"
	"regexp"
	"strings"
)

// PinnedReleaseKey is the `global_settings` key holding the pinned release
// tag. The dot matches the `update.git_url` precedent (B272.5) rather than
// the `update_schedule_*` underscore form.
const PinnedReleaseKey = "update.pinned_release"

// releaseTagPattern accepts a RELEASE tag only: `v1.5.94`, `1.5.94`,
// `v0.33.1.24`. Anchored, so a branch (`main`), a raw commit (`4b2186b`), a
// git-describe label (`v1.5.94-21-gd028f842`), the updater's own
// `skygate-pre-update-<sha>` tag and any floating ref are rejected by
// construction — pinning those would make "all instances run the same
// version" unprovable, which is the entire point of the setting.
var releaseTagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?$`)

// NormalizePinnedRelease canonicalises a release tag to the `vX.Y.Z` form.
//
// Returns ("", false) for anything that is not a release tag, so callers can
// reject it loudly instead of storing a value no instance can check out.
func NormalizePinnedRelease(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", false
	}
	if !releaseTagPattern.MatchString(t) {
		return "", false
	}
	return "v" + strings.TrimPrefix(t, "v"), true
}

// ReleaseOfBuildLabel extracts the release part of a running build label.
//
// `Service.BuildVersion` is `vX.Y.Z+<sha>` (ldflags) and a `git describe`
// label adds a `-N-g<sha>` suffix; both are the SAME release as the bare tag.
// Anything that is not a release label (a `v<sha>` dev label such as
// `ve2d0b9e+e2d0b9e`, an empty string) returns "" — callers must treat that
// as "unknown", never as "matches the pin".
func ReleaseOfBuildLabel(build string) string {
	s := strings.TrimSpace(build)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "+- "); i >= 0 {
		s = s[:i]
	}
	tag, ok := NormalizePinnedRelease(s)
	if !ok {
		return ""
	}
	return tag
}

// PinnedTargetFor is the scheduled updater's decision for a stored pin, kept
// pure so it can be pinned by a unit test instead of only by a live tick.
//
// Returns (tag, true) when this instance must be moved to the pinned release,
// and ("", false) when it already runs it (nothing to do) or when the stored
// value is not a release tag.
//
// Note the asymmetry with the "latest" path: the pin is applied even when it
// is OLDER than the running build. That is deliberate — pinning a known-good
// release is how an operator pulls a host that drifted forward back in line
// ("versions must match"), and refusing a downgrade would leave exactly the
// divergence the setting exists to remove.
func PinnedTargetFor(pin, buildVersion string) (string, bool) {
	tag, ok := NormalizePinnedRelease(pin)
	if !ok {
		return "", false
	}
	if ReleaseOfBuildLabel(buildVersion) == tag {
		return "", false
	}
	return tag, true
}

// PinnedReleaseFromDB reads the pinned release from `global_settings`.
//
// A missing row, a DB error and an unparsable value all return "" — the
// caller then follows the latest release, i.e. the pre-B346 behaviour. The
// admin page is responsible for telling the operator that a stored value is
// unusable; the workers stay silent rather than refusing to update.
func PinnedReleaseFromDB(db *sql.DB) string {
	if db == nil {
		return ""
	}
	raw, err := getGlobalSetting(db, PinnedReleaseKey, "")
	if err != nil {
		return ""
	}
	tag, ok := NormalizePinnedRelease(raw)
	if !ok {
		return ""
	}
	return tag
}
