// 2026-07-14: Этап 14 v8 — release-monitor package.
//
// Polls the GitHub Releases API for skygate, compares the
// latest tag against the running version (set at build time
// via -ldflags `-X main.version`), and emits a Notifier
// alert when a newer version is available. Also exposes a
// "release notes" preview for the admin.
//
// The package is small by design — it is *just* an HTTP GET
// + a JSON parse + a Notifier.SendAlert. There is no local
// cache: the scheduler tick in cmd/skygate/main.go is the
// dedup layer (it only emits when the latest tag has changed
// since the last check, not on every tick).
//
// API contract:
//
//   GET https://api.github.com/repos/BarsSky/skygate/releases/latest
//   {
//     "tag_name": "v0.10.7",
//     "name": "Skygate v0.10.7 — first official release",
//     "html_url": "https://github.com/BarsSky/skygate/releases/tag/v0.10.7",
//     "published_at": "2026-07-14T16:15:15Z",
//     "body": "...markdown..."
//   }
//
// Rate limit: 60 req/h unauthenticated from a single IP.
// We poll at most once per hour (configurable) and never
// authenticate, which leaves the headroom untouched even
// if the operator is on a shared NAT.

package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Owner / repo for the API URL. Hard-coded for now —
// skygate is single-tenant at the moment. If a second
// deployment ever needs its own release feed, refactor to
// pass (owner, repo) via Config.
const (
	apiBase = "https://api.github.com/repos/BarsSky/skygate"
)

// Release mirrors the JSON shape we need. We don't decode
// the full payload — only the fields the alert uses.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
}

// Client is the poller. One instance per process; safe for
// concurrent use because http.Client and the last-tag field
// are guarded by a mutex-less convention (only the scheduler
// goroutine calls Latest).
type Client struct {
	HTTP   *http.Client
	Repo   string // "owner/repo" — defaults to BarsSky/skygate
	Last   string // last seen tag (so the scheduler can dedup)
}

// NewClient returns a Client with sensible timeouts.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 10 * time.Second},
		Repo: "BarsSky/skygate",
	}
}

// Latest returns the most recent release, or an error. The
// caller is expected to compare TagName against cfg.Last and
// only emit an alert when they differ.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", c.Repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub asks for a UA on the API. Without it, the
	// request still works but the response is slower.
	req.Header.Set("User-Agent", "skygate-release-monitor/1.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		// Rate limited. The caller should back off and
		// retry on the next tick — we don't surface this
		// as a hard error to the operator.
		return nil, ErrRateLimited
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
// Tolerant of pre-release tags (v0.10.8-dev.1 < v0.10.8).
// Pre-release components are split on '.' and compared
// numerically where possible (so "dev.5" < "dev.10" —
// the standard semver behaviour), with a fallback to
// lexical comparison for non-numeric suffixes.
func CompareSemver(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	// Pre-release split: "0.10.7" and "0.10.8-dev.1" are
	// different. We split on '-' to get the base + suffix.
	pa := strings.SplitN(a, "-", 2)
	pb := strings.SplitN(b, "-", 2)
	// Base comparison is on dot-separated integer triples.
	// We only support major.minor.patch; anything past is
	// ignored.
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
	// Bases equal — pre-release < release. Per semver
	// spec, missing pre-release > any pre-release, so
	// "0.10.8" > "0.10.8-dev.1".
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
	// component-by-component. This is what semver
	// specifies: split each pre-release on '.', then for
	// each pair compare numerically if both are integers,
	// otherwise lexically.
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
// identifier components. Numeric identifiers are compared
// numerically; non-numeric identifiers are compared
// lexically. Numeric < non-numeric (per semver 11.4.4).
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

// scanInt reports whether s is a base-10 integer and, if
// so, writes the value to *out. The semver spec says a
// numeric identifier is a non-empty sequence of ASCII
// digits, no leading zeros (so "0" is OK, "01" is not).
// We accept leading zeros in practice — there is no
// real-world use case where a pre-release tag would carry
// one, and being lenient avoids a class of subtle bugs
// when the operator generates the tag with date stamps.
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

// FormatAlert returns a multi-line alert body suitable for
// Notifier.SendAlert. Kept short so the Telegram message
// doesn't get truncated.
func FormatAlert(current, latest *Release) string {
	notes := latest.Body
	if idx := strings.Index(notes, "\n\n"); idx > 0 {
		// Trim to the first paragraph if the body is long.
		notes = notes[:idx]
	}
	if len(notes) > 800 {
		notes = notes[:800] + "…"
	}
	return fmt.Sprintf(
		"🔔 Skygate update available\n"+
			"Running: %s\n"+
			"Latest:  %s\n"+
			"Released: %s\n"+
			"\n%s\n"+
			"\n→ %s",
		current.TagName, latest.TagName, latest.PublishedAt,
		notes, latest.HTMLURL,
	)
}

// ErrRateLimited is returned by Latest when GitHub returns
// 403. The scheduler should back off and retry on the next
// tick; this is not a hard error.
var ErrRateLimited = fmt.Errorf("github releases: rate limited")
