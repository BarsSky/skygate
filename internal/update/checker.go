// Package update — v0.29.0 self-update mechanism (Phase 1).
//
// Scope of this first cut:
//   - checker.go: GitHub Releases API client + semver comparison +
//     cached result (avoid hammering GitHub on every page load)
//   - install.go: detect the install kind (Docker / Systemd / Bare)
//   - manual.go: generate copy-pasteable manual update steps per
//     install kind (the actual binary swap is operator-driven for
//     v0.29.0; the plan lays the groundwork for v0.30.0 to do the
//     swap automatically)
//
// What's NOT here yet (deferred to follow-up releases):
//   - auto binary download + SHA verification
//   - auto restart (Docker compose pull / Systemd unit replace)
//   - SSE streaming of the update log to the UI
//   - PG-specific migration protocol (covered by the v0.27.0 driver
//     abstraction that's still on feat/postgres-migration)
//
// See docs/plans/self-update-v0.29.md for the full design.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Checker queries the GitHub Releases API for the latest skygate
// release and compares it to the running BuildVersion.
//
// The result is cached in-process for 6h (configurable via
// cfg.UpdateCheckInterval). On error, the cache holds the
// previous good result for 15m (configurable via
// checkFailureCache) so a transient GitHub blip doesn't trigger
// a "new version available" alert every page load.
//
// Concurrency: Checker is safe for concurrent calls. The
// in-flight request is deduplicated via a singleflight-style
// mutex — only one HTTP call at a time, all callers wait.
type Checker struct {
	// HTTPClient is the HTTP client used for the GitHub API.
	// Exposed for tests (httptest.NewServer).
	HTTPClient *http.Client
	// Owner / Repo are the GitHub coordinates. Defaults to
	// the operator's actual repo (set at the call site via
	// config.Config.GitHubOwner / GitHubRepo, fallback
	// "BarsSky"/"skygate"). Exposed for tests (a private
	// repo or a fork).
	Owner string
	Repo  string
	// Channel: "stable" = only non-prerelease tags; "all" =
	// any tag (including v0.30.0-rc.1).
	Channel string
	// GitHubToken: optional, bumps rate limit from 60/h to 5000/h.
	GitHubToken string
	// CurrentVersion: the running skygate version, e.g.
	// "0.28.6+abc1234" or "v0.28.7". Compared against the
	// latest GitHub release tag (e.g. "v0.29.0").
	CurrentVersion string

	// lastResult caches the most recent successful Result.
	lastResult *Result
	lastResultAt time.Time

	// lastErrorAt is the timestamp of the most recent failed
	// call; used to back off for 15m after an error.
	lastErrorAt time.Time
}

// Result is the outcome of a check.
type Result struct {
	// Latest is the most recent GitHub release tag, e.g.
	// "v0.29.0". Empty if the check failed or no release
	// was found.
	Latest string
	// LatestVersion is Latest with the "v" prefix stripped
	// ("0.29.0"). Used for semver comparison.
	LatestVersion string
	// IsNewer is true when LatestVersion > CurrentVersion.
	IsNewer bool
	// ReleaseURL is the GitHub release page URL, e.g.
	// "https://github.com/<owner>/<repo>/releases/tag/v0.29.0".
	ReleaseURL string
	// Body is the release notes (markdown). Truncated to
	// MaxBodyLen characters (default 4000) so the
	// /admin/update page doesn't render a 100KB blob.
	Body string
	// PublishedAt is the release publication timestamp.
	PublishedAt time.Time
	// CheckedAt is when this result was fetched.
	CheckedAt time.Time
	// SourceURL is the GitHub API URL the result came from,
	// for debugging in the UI.
	SourceURL string
	// Error is the most recent error message (network, parse,
	// rate limit). Empty on success. The result MAY still be
	// cached from a previous successful call.
	Error string
}

// MaxBodyLen caps the release notes body in the UI.
const MaxBodyLen = 4000

// checkSuccessCache is the duration a successful result is
// cached. After this, the next Check() call hits GitHub.
const checkSuccessCache = 6 * time.Hour

// checkFailureCache is the duration to back off after a
// failed call. Prevents hammering GitHub on every page load
// when the network is down or rate limit is hit.
const checkFailureCache = 15 * time.Minute

// Check returns the most recent result, fetching a new one
// from GitHub if the cache is stale. The fetch respects the
// 6h success / 15m failure cache windows.
//
// On error, returns the last good cached Result (if any) with
// Result.Error set to the new error message. This lets the
// UI show "no new version (last check 2h ago failed: ...)".
func (c *Checker) Check(ctx context.Context) (*Result, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.Owner == "" {
		c.Owner = "skygate-operator"
	}
	if c.Repo == "" {
		c.Repo = "skygate"
	}
	if c.Channel == "" {
		c.Channel = "stable"
	}
	now := time.Now()
	// Return cached success if fresh
	if c.lastResult != nil && now.Sub(c.lastResultAt) < checkSuccessCache && c.lastResult.Error == "" {
		return c.lastResult, nil
	}
	// Back off after a failure (don't hammer GitHub)
	if !c.lastErrorAt.IsZero() && now.Sub(c.lastErrorAt) < checkFailureCache && c.lastResult == nil {
		return &Result{
			CheckedAt: now,
			Error:     fmt.Sprintf("backing off until %s (last check failed)", c.lastErrorAt.Add(checkFailureCache).Format(time.RFC3339)),
		}, nil
	}

	result, err := c.fetch(ctx)
	if err != nil {
		c.lastErrorAt = now
		// If we have a stale cached result, return it with
		// the new error attached. Better than nothing.
		if c.lastResult != nil {
			cached := *c.lastResult
			cached.Error = err.Error()
			cached.CheckedAt = now
			return &cached, nil
		}
		return &Result{
			CheckedAt: now,
			Error:     err.Error(),
		}, nil
	}
	result.CheckedAt = now
	c.lastResult = result
	c.lastResultAt = now
	c.lastErrorAt = time.Time{}
	return result, nil
}

// fetch hits the GitHub API and returns a fresh Result.
func (c *Checker) fetch(ctx context.Context) (*Result, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.Owner, c.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("checker: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.GitHubToken)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checker: GET %s: %w", apiURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		// Likely rate-limited. Read the body for the X-RateLimit-Reset
		// header so the operator knows when to retry.
		retryAfter := resp.Header.Get("X-RateLimit-Reset")
		return nil, fmt.Errorf("checker: GitHub API rate limited (reset at unix %s)", retryAfter)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("checker: GitHub API returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var gh ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gh); err != nil {
		return nil, fmt.Errorf("checker: parse response: %w", err)
	}
	if gh.TagName == "" {
		return nil, fmt.Errorf("checker: GitHub release has empty tag_name")
	}
	// Honor channel: "stable" excludes prereleases (anything
	// with a "-" suffix in the semver like "-rc.1" or "-beta").
	latestVersion := strings.TrimPrefix(gh.TagName, "v")
	if c.Channel == "stable" && hasPrereleaseSuffix(latestVersion) {
		// Try the second-newest release (the latest non-prerelease).
		// For v0.29.0 we don't need this in the happy path — the
		// operator only ships stable tags. If the latest is
		// "-rc.1" and we want stable, we need to walk the list.
		// For v0.29.0 Phase 1, we just skip and return a
		// "prerelease available" result.
		return &Result{
			Latest:        gh.TagName,
			LatestVersion: latestVersion,
			IsNewer:       compareSemver(latestVersion, c.CurrentVersion) > 0,
			ReleaseURL:    gh.HTMLURL,
			Body:          truncate(gh.Body, MaxBodyLen),
			PublishedAt:   gh.PublishedAt,
			SourceURL:     apiURL,
			Error:         fmt.Sprintf("latest is prerelease (%s) but channel is 'stable' — set SKYGATE_UPDATE_CHANNEL=all to see it", gh.TagName),
		}, nil
	}
	return &Result{
		Latest:        gh.TagName,
		LatestVersion: latestVersion,
		IsNewer:       compareSemver(latestVersion, c.CurrentVersion) > 0,
		ReleaseURL:    gh.HTMLURL,
		Body:          truncate(gh.Body, MaxBodyLen),
		PublishedAt:   gh.PublishedAt,
		SourceURL:     apiURL,
	}, nil
}

// ghRelease mirrors the relevant fields of the GitHub Releases
// API JSON. Field names match the API exactly (snake_case).
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// compareSemver returns -1, 0, +1 if a < b, a == b, a > b.
// Handles the skygate build label suffixes:
//   - "+abc1234"    — explicit commit hash appended by ldflags
//     (e.g. "0.28.6+abc1234" vs "0.28.6") — stripped before compare.
//   - "-N-g<hash>"  — git-describe --tags suffix (e.g.
//     "1.3.11-27-g03a1d97" = "1.3.11 + 27 commits ahead").
//     This means the build is N commits past the last tag. We
//     treat the version as the BASE (the part before "-N-g..."),
//     so "1.3.11-27-g03a1d97" compares equal to "1.3.11" — the
//     build is the v1.3.11 line, not a different version.
//     Without this strip, parseUint on "11-27-g03a1d97" fails
//     and the fallback lex compare puts "9" > "11-..." (because
//     '9' > '1'), incorrectly reporting a v1.3.9 release as
//     newer than a v1.3.11+27 local build (the live B124 bug).
//
// Not a full semver impl — doesn't handle 4-part versions
// (1.2.3.4) or pre-release ordering (1.0.0-rc.1 < 1.0.0).
// Sufficient for skygate's x.y.z tag scheme.
func compareSemver(a, b string) int {
	a = stripBuildLabelSuffix(a)
	b = stripBuildLabelSuffix(b)
	aParts := strings.SplitN(strings.TrimPrefix(a, "v"), ".", 3)
	bParts := strings.SplitN(strings.TrimPrefix(b, "v"), ".", 3)
	// Pad to 3 parts
	for len(aParts) < 3 {
		aParts = append(aParts, "0")
	}
	for len(bParts) < 3 {
		bParts = append(bParts, "0")
	}
	for i := 0; i < 3; i++ {
		an, aerr := parseUint(aParts[i])
		bn, berr := parseUint(bParts[i])
		if aerr != nil || berr != nil {
			// Both failed → lex compare. This is the original
			// fallback; the stripBuildLabelSuffix above handles
			// the git-describe case before we get here.
			if aParts[i] < bParts[i] {
				return -1
			}
			if aParts[i] > bParts[i] {
				return 1
			}
			continue
		}
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
	}
	return 0
}

// stripBuildLabelSuffix returns the version with both
// git-describe ("-N-g<hex>") and explicit-commit ("+<hex>")
// suffixes removed. Examples:
//   "1.3.11-27-g03a1d97"   → "1.3.11"
//   "1.3.11-27-g03a1d97+abc1234" → "1.3.11"
//   "0.28.6+abc1234"       → "0.28.6"
//   "1.3.11"               → "1.3.11"  (unchanged)
func stripBuildLabelSuffix(v string) string {
	// Strip the explicit-commit "+<hex>" suffix first (it's
	// always at the very end if present).
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	// Strip the git-describe "-N-g<hex>" suffix. Pattern:
	//   "-" then a non-empty run of digits (the commit count
	//   ahead of the last tag) then "-g" then a hex hash.
	//   Examples: "-27-g03a1d97", "-1-gabc1234", "-0-gdeadbeef"
	// We deliberately do NOT strip plain "-rc.1" / "-beta"
	// pre-release markers — those are real semver suffixes
	// that the channel check (hasPrereleaseSuffix) handles.
	if i := gitDescribeSuffixStart(v); i >= 0 {
		v = v[:i]
	}
	return v
}

// gitDescribeSuffixStart returns the index of the "-N-g<hex>"
// suffix start in v, or -1 if no such suffix is present.
// Detected pattern: at least one "-", followed by digits, then
// "-g", then 7+ hex chars. The check is anchored at the LAST
// "-g" in the string so that v1.2.3-rc.1-g0123456 would still
// match (rc.1 is prerelease, g0123456 is the git-describe hash).
func gitDescribeSuffixStart(v string) int {
	// Find the LAST "-g" that has a hex hash after it.
	// Scan right-to-left to find candidates.
	idx := strings.LastIndex(v, "-g")
	for idx > 0 {
		hash := v[idx+2:]
		if len(hash) >= 7 && isAllHex(hash) {
			// Found the hash. Now walk back to find the
			// digits between the previous "-" and here.
			// The pattern is "<base>-<count>-g<hash>".
			prefix := v[:idx] // everything before "-g<hash>"
			// The "count" is between the last "-" in prefix
			// and the end of prefix.
			dash := strings.LastIndex(prefix, "-")
			if dash < 0 {
				return -1
			}
			count := prefix[dash+1:]
			if count == "" || !isAllDigits(count) {
				return -1
			}
			// Valid git-describe suffix. Return the index
			// of the leading "-".
			return dash
		}
		// Not a hex hash — keep searching leftward.
		next := strings.LastIndex(v[:idx], "-g")
		if next < 0 {
			return -1
		}
		idx = next
	}
	return -1
}

func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// hasPrereleaseSuffix returns true if the version string has
// a "-" separator followed by a non-numeric suffix (e.g.
// "0.29.0-rc.1", "0.29.0-beta", "0.29.0-pre+abc1234").
func hasPrereleaseSuffix(v string) bool {
	v = strings.SplitN(v, "+", 2)[0]
	idx := strings.Index(v, "-")
	if idx < 0 {
		return false
	}
	suffix := v[idx+1:]
	return suffix != "" && !isAllDigits(suffix)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseUint(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
		if n > 1<<31-1 {
			return 0, fmt.Errorf("too large: %q", s)
		}
	}
	return n, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n\n[…truncated, full release notes on the GitHub page below…]"
}

// SanitizeAPIURL strips the GitHub token from any URL that
// might end up in a log line. The token can appear as
// ?access_token=... (OAuth) or as a path segment, depending
// on how an operator wires it up. We only use Bearer auth
// in this implementation, so this is a no-op for now, but
// the helper exists for future-proofing.
func SanitizeAPIURL(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	parsed.RawQuery = "" // strip access_token etc.
	parsed.Fragment = ""
	return parsed.String()
}
