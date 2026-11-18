// check_endpoint_b491.go — the update check must not depend on ONE host (TD-39 / B491).
//
// # THE MEASURED PROBLEM
//
// /admin/update printed
//
//	checker: GET https://api.github.com/…: context deadline exceeded
//
// on the production host while a plain `curl` from the same service user fetched the same URL in
// 0.2 s (measured 2026-11-17, B486): the egress to api.github.com is intermittently filtered (DPI),
// not broken. B486 made the failure HONEST (classification + layout); it deliberately did not fix the
// reachability, and registered that as TD-39: the endpoint itself must be configurable, the way the
// GIT remote already is (`update.git_url`, B272.5).
//
// # THE DESIGN
//
// The checker keeps an ORDERED list of release-JSON endpoints. They are tried in order; the first
// answer wins, every failure is logged with its URL, and only when ALL of them fail does the
// existing git-tags door (B445) get its turn. That is the same "several doors to one answer" shape
// the tag fallback already has, so a host that cannot reach GitHub at all still says something true.
//
// An endpoint is either:
//
//   - the GitHub REST shape — `https://api.github.com/repos/{owner}/{repo}/releases/latest` (default);
//   - ANY URL returning the same release JSON — a proxy, a corporate mirror, a static file served by
//     the operator's own webserver (`https://mirror.example.com/skygate/latest.json`).
//
// `{owner}` and `{repo}` are substituted before the request, so one configured template follows a
// fork or a renamed repository. Only http/https are accepted: a `file://` or `gopher://` endpoint
// would be a local-file read dressed as a version check.
package update

import (
	"fmt"
	"strings"
)

// DefaultCheckEndpoint is the read-only GitHub releases endpoint. It is a TEMPLATE: {owner} and
// {repo} are substituted from the checker's coordinates.
const DefaultCheckEndpoint = "https://api.github.com/repos/{owner}/{repo}/releases/latest"

// SourceMirror marks an answer that came from a CONFIGURED endpoint (a mirror or proxy) rather than
// api.github.com. The value is exact — the JSON shape is the same — but the operator should know the
// answer did not come from GitHub directly, because a stale mirror is the failure mode that matters.
const SourceMirror = "mirror-endpoint"

// ParseCheckEndpoints validates and normalises a configured endpoint list. Accepts a comma- or
// whitespace-separated string (an env var is usually one line, a DB setting may be several),
// trims everything, drops empties and duplicates while KEEPING ORDER (the order IS the priority),
// and rejects anything that is not http(s).
//
// An empty input is not an error: it means "use the default", which the caller expresses by leaving
// the list empty.
func ParseCheckEndpoints(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		e := strings.TrimSpace(f)
		if e == "" {
			continue
		}
		lower := strings.ToLower(e)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return nil, fmt.Errorf("endpoint %q is not http(s) — only an HTTP(S) release JSON can be checked safely", e)
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out, nil
}

// CheckEndpointsFor resolves which endpoints a check should use, with the precedence the rest of the
// project already uses for the git mirror (B272.5): environment first (it is the break-glass knob an
// operator can set without the panel), then the database setting the panel writes, then the default.
//
// envRaw and dbValue may both be empty; the result is then a single default endpoint.
func CheckEndpointsFor(envRaw, dbValue string) ([]string, error) {
	for _, raw := range []string{envRaw, dbValue} {
		eps, err := ParseCheckEndpoints(raw)
		if err != nil {
			return nil, err
		}
		if len(eps) > 0 {
			return eps, nil
		}
	}
	return []string{DefaultCheckEndpoint}, nil
}

// endpointURLs expands the configured templates for THIS checker (owner/repo) and always returns at
// least one URL, so the caller never has to special-case an empty list.
func (c *Checker) endpointURLs() []string {
	raw := c.Endpoints
	if len(raw) == 0 {
		raw = []string{DefaultCheckEndpoint}
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if c.Owner != "" {
			e = strings.ReplaceAll(e, "{owner}", c.Owner)
		}
		if c.Repo != "" {
			e = strings.ReplaceAll(e, "{repo}", c.Repo)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.Owner, c.Repo))
	}
	return out
}

// endpointSource names where an answer came from, so /admin/update can say whether the value was read
// from GitHub itself or from a mirror the operator configured (a stale mirror is the failure mode that
// matters, and a page that cannot tell the difference hides it).
func endpointSource(rawURL string) string {
	if isGitHubAPIHost(rawURL) {
		return SourceAPI
	}
	return SourceMirror
}

// isGitHubAPIHost reports whether a URL points at the real GitHub API, which is the only case where
// the API-specific headers (version pin, bearer token) belong on the request. Sending a GitHub token
// to a MIRROR would hand the credential to a third party — the reason this check exists at all.
func isGitHubAPIHost(rawURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(lower, "https://api.github.com/") ||
		strings.HasPrefix(lower, "http://api.github.com/") ||
		strings.HasPrefix(lower, "https://github.com/") ||
		strings.HasPrefix(lower, "http://github.com/")
}
