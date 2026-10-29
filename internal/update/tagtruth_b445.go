// tagtruth_b445.go — B445 (2026-10-29) — THE LATEST VERSION MUST NOT DEPEND ON ONE RATE-LIMITED
// DOOR.
//
// Live on the reference install (measured this session): `api.github.com/rate_limit` reported
// core limit 60 / remaining 0, and the journal carried
//
//	release-monitor: GitHub rate limit hit — the next check is in ≤1h0m0s; set
//	SKYGATE_GITHUB_TOKEN to raise 60 requests/hour to 5000
//
// every hour for two days. With the anonymous quota dead, `GET /releases/latest` answers 403,
// the checker has no fresh answer, and the operator's page could not show v1.5.190…v1.5.195 —
// the install stayed on v1.5.189 while six releases were published, and the SCHEDULED update
// refused to act because `result.Latest` was empty. The remedy the page offered was an env
// var, i.e. work for the operator.
//
// There is a second door that needs no API quota at all: `git ls-remote --tags`, which the
// updater's own git already speaks. This file turns the git ref list into the same answer the
// API would have given (the highest semver tag), so a rate-limited install keeps working — and
// the page can say WHERE the answer came from instead of showing nothing.
package update

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Result sources, rendered on /admin/update so the operator knows how much to trust the value.
const (
	// SourceAPI — the answer came from the GitHub Releases API (notes included).
	SourceAPI = "github-api"
	// SourceGitTags — the API was unusable, so the answer came from the repository's tags.
	// The VERSION is exact; the release NOTES are missing.
	SourceGitTags = "git-tags"
)

// HighestSemverTag picks the newest semver tag out of a `git ls-remote --tags` listing.
//
// Accepted shapes, all of which the real listing contains:
//
//	refs/tags/v1.5.195
//	refs/tags/v1.5.195^{}      (the dereferenced object of an ANNOTATED tag — a duplicate)
//	v1.5.195                   (a bare tag, for tests and `git tag` output)
//
// `channel` is the same knob the API path honours: "stable" skips prerelease suffixes
// (`-rc.1`), anything else keeps them. Non-semver tags (a release candidate branch name, a
// hand-made `backup-…` tag, `v1.5` without a patch) are skipped rather than guessed at.
func HighestSemverTag(refs []string, channel string) (tag, version string, ok bool) {
	seen := map[string]bool{}
	for _, raw := range refs {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		// Strip the ref prefix and the annotated-tag dereference marker.
		if i := strings.Index(t, "refs/tags/"); i >= 0 {
			t = t[i+len("refs/tags/"):]
		}
		t = strings.TrimSuffix(t, "^{}")
		if i := strings.IndexAny(t, " \t"); i >= 0 {
			t = t[:i] // `ls-remote --refs` lines may carry the peeled hash after whitespace
		}
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		v := strings.TrimPrefix(t, "v")
		if !looksLikeSemver(v) {
			continue
		}
		if channel == "stable" && hasPrereleaseSuffix(v) {
			continue
		}
		if !ok || compareSemver(v, version) > 0 {
			tag, version, ok = t, v, true
		}
	}
	return tag, version, ok
}

// looksLikeSemver is the gate that keeps a junk tag from becoming "the latest version": three
// numeric parts separated by dots. `v1.5` and `backup-2026-10-29` are not versions.
func looksLikeSemver(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for i, p := range parts {
		// The third part may carry a prerelease suffix ("195-rc.1"): validate the numeric head.
		head := p
		if i == 2 {
			if j := strings.IndexAny(p, "-+"); j >= 0 {
				head = p[:j]
			}
		}
		if head == "" {
			return false
		}
		if _, err := strconv.Atoi(head); err != nil {
			return false
		}
	}
	return true
}

// GitLsRemoteTags lists the tags of `remote` without touching the GitHub API, so it works on a
// rate-limited or air-gapped host. It deliberately does not use the working tree: a detour
// through `git fetch` would mutate the checkout the updater is about to switch.
func GitLsRemoteTags(ctx context.Context, dir, remote string) ([]string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}
	// 15 s: a listing is tiny, and a hung network must not hold the page.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", remote)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote --tags %s: %w", remote, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	refs := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			refs = append(refs, l)
		}
	}
	return refs, nil
}
