// 2026-07-20: v0.20.0 — headscale version monitor.
//
// Polls the GitHub Releases API for juanfont/headscale, compares
// the latest tag against the operator's pinned version (from
// SKYGATE_HEADSCALE_VERSION_PIN env or auto-detected via
// `headscale version` if accessible), and emits a Notifier
// alert + writes a row to headscale_releases when a newer
// version appears.
//
// The package is structurally a sibling of internal/release
// (which monitors skygate itself). The two packages share
// CompareSemver via a tiny duplication rather than a third
// shared package — semver is ~50 lines and the rules diverge
// in ways that have already bit us once (pre-release
// handling, "v" prefix, "0.30" vs "0.30.0" normalisation).
// Keeping them separate is easier than coupling.
//
// API contract:
//
//   GET https://api.github.com/repos/juanfont/headscale/releases/latest
//   {
//     "tag_name": "v0.30.0",
//     "name":     "Headscale v0.30.0",
//     "html_url": "https://github.com/juanfont/headscale/releases/tag/v0.30.0",
//     "published_at": "2026-XX-XXTXX:XX:XXZ",
//     "body":    "...markdown changelog..."
//   }
//
// Rate limit: 60 req/h unauthenticated. We poll at most
// once per 24h (configurable) which leaves 56/60 unused
// for any concurrent one-off curl from the operator.
//
// Why GitHub and not Docker Hub: Docker Hub's tag list API
// returns size/digest metadata but NO changelog or
// release-notes field. The admin needs the release notes to
// decide whether to upgrade. GitHub has both. We use Docker
// Hub only as a secondary sanity check (the release must
// also exist as a `:vX.Y.Z` image) — but that's a future
// v0.21.0 enhancement, not v0.20.0 scope.

package headscale_version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Repo is the GitHub owner/repo to monitor. Hard-coded
// because headscale is a single project. If a fork ever
// matters, refactor to a config field.
const Repo = "juanfont/headscale"

// Release mirrors the GitHub API JSON shape we need. We
// don't decode the full payload — only the fields the
// alert + UI use.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
}

// Client is the poller. Safe for concurrent use because
// the only mutable state is http.Client (thread-safe by
// stdlib contract) and the last-tag string is owned by
// the scheduler goroutine.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a Client with sensible timeouts.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

// Latest returns the most recent headscale release.
// Caller compares TagName against the operator's pinned
// version and only emits when they differ.
//
// The user-agent is required by GitHub's API; without it
// the response is slower (and the rate limit tighter).
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", Repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "skygate-headscale-monitor/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		// Rate limited. Caller (the Monitor goroutine)
		// treats this as a no-op and retries on the next
		// tick — surface as a typed error so the caller
		// can suppress the standard "poll failed" log.
		return nil, ErrRateLimited
	}
	if resp.StatusCode == http.StatusNotFound {
		// No releases published yet (shouldn't happen for
		// headscale, but defensive). Treat as "no
		// signal" rather than an error.
		return nil, ErrNoReleases
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("github releases: decode: %w", err)
	}
	if r.TagName == "" {
		return nil, fmt.Errorf("github releases: empty tag_name")
	}
	return &r, nil
}

// CompareSemver returns -1/0/+1 if a is less/equal/greater
// than b by semver. Strips the leading 'v' if present.
// Tolerant of missing patch ("0.30" == "0.30.0") and
// pre-release tags (v0.30.0-dev.1 < v0.30.0).
//
// This is a near-copy of internal/release.CompareSemver;
// we intentionally keep them separate because the
// headscale project's tag conventions (no leading "v"
// in some 0.20.x releases) have already surprised us
// once. If a third caller ever needs semver, extract
// to internal/semver.
func CompareSemver(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	// Pre-release split: "0.30.0" and "0.30.0-dev.1"
	// are different. We split on '-' to get the base +
	// suffix.
	pa := strings.SplitN(a, "-", 2)
	pb := strings.SplitN(b, "-", 2)
	// Base comparison on dot-separated integer triples.
	as := strings.Split(pa[0], ".")
	bs := strings.Split(pb[0], ".")
	for i := 0; i < 3; i++ {
		if i >= len(as) {
			as = append(as, "0")
		}
		if i >= len(bs) {
			bs = append(bs, "0")
		}
		var ai, bi int
		fmt.Sscanf(as[i], "%d", &ai)
		fmt.Sscanf(bs[i], "%d", &bi)
		if ai < bi {
			return -1
		}
		if ai > bi {
			return +1
		}
	}
	// Bases equal — pre-release < release (per semver
	// 11.4.3: a pre-release version has lower precedence
	// than the associated normal version).
	if len(pa) == 2 && len(pb) < 2 {
		return -1
	}
	if len(pa) < 2 && len(pb) == 2 {
		return +1
	}
	if len(pa) < 2 && len(pb) < 2 {
		return 0
	}
	// Both have pre-release suffixes. Compare
	// component-by-component (semver 11.4.4).
	preA := strings.Split(pa[1], ".")
	preB := strings.Split(pb[1], ".")
	for i := 0; i < len(preA) && i < len(preB); i++ {
		c := comparePreReleaseComponent(preA[i], preB[i])
		if c != 0 {
			return c
		}
	}
	switch {
	case len(preA) < len(preB):
		return -1
	case len(preA) > len(preB):
		return +1
	}
	return 0
}

// comparePreReleaseComponent compares two pre-release
// identifier components. Numeric < non-numeric (per
// semver 11.4.4).
func comparePreReleaseComponent(a, b string) int {
	var ai, bi int
	aIsNum := scanInt(a, &ai)
	bIsNum := scanInt(b, &bi)
	switch {
	case aIsNum && bIsNum:
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return +1
		}
		return 0
	case aIsNum && !bIsNum:
		return -1
	case !aIsNum && bIsNum:
		return +1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return +1
	}
	return 0
}

// scanInt reports whether s is a non-empty sequence of
// ASCII digits and, if so, writes the value to *out.
func scanInt(s string, out *int) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := fmt.Sscanf(s, "%d", out)
	return err == nil
}

// IsBreaking returns true if the version bump from
// current→latest crosses a major or minor boundary
// (i.e. is not a patch release). Patch releases are
// usually safe to deploy same-day; minor/major ones
// need the admin's attention.
//
// Examples:
//
//	IsBreaking("0.29.1", "0.29.2") = false (patch)
//	IsBreaking("0.29.2", "0.30.0") = true  (minor)
//	IsBreaking("0.29.2", "1.0.0")  = true  (major)
//	IsBreaking("0.29.2", "0.29.2") = false (same)
//
// Used by the UI to colour-code the banner (red for
// breaking, blue for safe) and by the alert prefix
// ("⚠" vs "🔔").
func IsBreaking(current, latest string) bool {
	if CompareSemver(current, latest) >= 0 {
		return false
	}
	cur := strings.Split(strings.TrimPrefix(current, "v"), ".")
	lat := strings.Split(strings.TrimPrefix(latest, "v"), ".")
	// major.major diff
	if cur[0] != lat[0] {
		return true
	}
	// minor.minor diff
	if len(cur) > 1 && len(lat) > 1 && cur[1] != lat[1] {
		return true
	}
	return false
}

// FormatAlert returns the multi-line alert body. Trims
// the changelog to the first paragraph and 800 chars
// so a long release-notes doesn't blow past Telegram's
// 4096-char message limit. The "→" line at the end is
// the GitHub release URL.
func FormatAlert(pinned, latest *Release, breaking bool) string {
	prefix := "🔔"
	if breaking {
		prefix = "⚠️"
	}
	notes := latest.Body
	if idx := strings.Index(notes, "\n\n"); idx > 0 {
		notes = notes[:idx]
	}
	if len(notes) > 800 {
		notes = notes[:800] + "…"
	}
	return fmt.Sprintf(
		"%s Headscale update available\n"+
			"Running: %s\n"+
			"Latest:  %s\n"+
			"Released: %s\n"+
			"\n%s\n"+
			"\n→ %s",
		prefix, pinned.TagName, latest.TagName, latest.PublishedAt,
		notes, latest.HTMLURL,
	)
}

// ErrRateLimited is returned by Latest when GitHub
// returns 403. The Monitor should back off and retry
// on the next tick; this is not a hard error.
var ErrRateLimited = fmt.Errorf("github releases: rate limited")

// ErrNoReleases is returned when the repo has no
// published releases yet (404). Defensive — headscale
// has releases since 0.16, so this should never fire
// in practice.
var ErrNoReleases = fmt.Errorf("github releases: no releases published")
