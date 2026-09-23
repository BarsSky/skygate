package headscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// B297 (2026-09-23) — ask the RUNNING headscale what version it is.
//
// # WHY
//
// Until B297 the only answer skygate had was `SKYGATE_HEADSCALE_VERSION_PIN`, a
// human declaration in the env file — internal/config/config.go says so out
// loud: "The pin is an env var (not auto-detected) because skygate doesn't shell
// into the headscale container. Auto-detect could come in a v0.21.0+". Three
// things go wrong when that declaration drifts from reality:
//
//  1. The update monitor compares the LATEST GitHub release against a value the
//     operator typed once. Live setup: `aro` runs headscale 0.29.0 while the
//     agent VM runs 0.29.3 — with a stale pin the "a newer headscale is
//     available" alert is wrong in whichever direction the pin is wrong, and
//     nothing anywhere says the two hosts even differ.
//  2. 0.29.x is NOT uniform in the API surface skygate depends on, and every one
//     of those differences is handled by a capability LADDER rather than a
//     version check: `POST /api/v1/node/{id}/approve_routes` is gone from REST
//     at 0.29.1 (routes.go), the REST expire path is broken at 0.29.2
//     (nodes.go), `grants[]` replaced `acls[]` at 0.29.0-beta.4, and 0.29.2 is
//     the version that rejects wildcards in `tagOwners` while requiring
//     `ip: ["*"]` (acl.go). Knowing the running version turns "which rung will
//     this host take, and why" from a guess into a fact.
//  3. A pin mismatch is silent. There is no page, no banner and no journal line
//     that says "the daemon answered 0.29.0 but you declared 0.29.2".
//
// # WHY A LADDER, NOT ONE CALL
//
// Same rule as the B294 live-policy read: the cheapest, most authoritative rung
// first, and every attempt is remembered so a failure names what was tried
// instead of claiming the version is unknown.
//
//  1. GET /api/v1/version — the gRPC-gateway GetVersion, authenticated with the
//     same key the rest of the client uses;
//  2. GET /version        — the unauthenticated root endpoint, when the build
//     serves it (no key needed, so it still answers while a key is being
//     rotated);
//  3. `headscale version` through the install-kind ladder (`runHeadscaleCLI`:
//     `docker exec` on a container host, the local binary on a native one) —
//     the only rung that still works when the API address itself is wrong.
//
// A rung that answers with something that is not a version is NOT a version: the
// next rung runs, and the failure text of every rung is kept for the operator.
const (
	// VersionViaAPI is rung 1.
	VersionViaAPI = "GET /api/v1/version"
	// VersionViaRoot is rung 2.
	VersionViaRoot = "GET /version"
	// VersionViaCLI is rung 3.
	VersionViaCLI = "headscale version"
)

// versionProbeTimeout bounds ONE rung. The whole probe is therefore at most
// ~3×this, which is why it is short: it runs during boot and on every monitor
// tick.
const versionProbeTimeout = 4 * time.Second

// maxVersionBodyBytes caps what a rung may buffer. A version document is a few
// dozen bytes; anything larger is a proxy error page or a login redirect.
const maxVersionBodyBytes = 4096

// ServerVersion is what the running headscale answered (or why nothing did).
type ServerVersion struct {
	// Version is the normalised version ("0.29.3"), empty when no rung produced
	// one. A leading "v" is stripped so it can be fed straight to
	// headscale_version.CompareSemver, which tolerates both spellings.
	Version string
	// Via names the rung that answered (one of the VersionVia* constants).
	Via string
	// CheckedAt is when the probe ran.
	CheckedAt time.Time
	// Tried is one line per rung that did NOT answer with a version. It is what
	// the operator sees when Version is empty.
	Tried []string
}

// Found reports whether a version was read.
func (s ServerVersion) Found() bool { return s.Version != "" }

// Reason explains an empty Version. Never empty for a failed probe, so a caller
// can log it without inventing a cause.
func (s ServerVersion) Reason() string {
	if s.Found() {
		return ""
	}
	if len(s.Tried) == 0 {
		return "the probe did not run"
	}
	return strings.Join(s.Tried, "; ")
}

// versionInText finds a dotted numeric version anywhere in a short text, with an
// optional leading "v" and an optional pre-release suffix. Requiring the dot is
// what keeps it from matching an HTTP code or a port number.
var versionInText = regexp.MustCompile(`\bv?(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.\-]+)?)\b`)

// versionStrict validates a candidate that came from a NAMED field (e.g. the
// "version" key of a JSON document), where a loose search is unnecessary.
var versionStrict = regexp.MustCompile(`^v?(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.\-]+)?)$`)

// normalizeServerVersion trims a candidate and drops a leading "v". It returns
// "" for anything that is not a dotted version, so a caller can never publish a
// commit hash or an error string as "the running version".
func normalizeServerVersion(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"'`))
	if s == "" {
		return ""
	}
	m := versionStrict.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// parseVersionBody extracts a version from an HTTP body: an explicit version
// field first (JSON object, any casing/snake_case spelling), a bare JSON string
// second, and a loose search of a SHORT body last. The loose path is length
// capped on purpose — a long HTML error page can contain version-looking numbers
// that have nothing to do with headscale.
func parseVersionBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	if trimmed[0] == '{' {
		var raw map[string]any
		if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
			for _, key := range []string{"version", "Version", "server_version", "serverVersion", "headscale_version"} {
				if v, ok := raw[key].(string); ok {
					if n := normalizeServerVersion(v); n != "" {
						return n
					}
				}
			}
		}
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(trimmed), &s); err == nil {
			if n := normalizeServerVersion(s); n != "" {
				return n
			}
		}
	}
	if len(trimmed) > 200 {
		return ""
	}
	if m := versionInText.FindStringSubmatch(trimmed); m != nil {
		return m[1]
	}
	return ""
}

// parseVersionCLI extracts the version from `headscale version` output.
//
// A SERVER line wins over anything else: the command reports the client binary's
// own version too, and the client's version is not the answer we are after (a
// stale `/usr/bin/headscale` next to a fresh container is a real layout). When
// no line mentions a server, the first version in the output is used.
func parseVersionCLI(out string) string {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "server") {
			continue
		}
		if m := versionInText.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	if m := versionInText.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// DetectServerVersion probes the running headscale and returns what it answered
// (or every rung it tried). It never returns an error: a failure is data the
// caller reports, because "unknown" must stay distinguishable from "declared".
func (c *Client) DetectServerVersion(ctx context.Context) ServerVersion {
	out := ServerVersion{CheckedAt: time.Now()}
	if c == nil || c.BaseURL == "" {
		out.Tried = append(out.Tried, "no headscale client configured (HEADSCALE_URL is empty)")
		return out
	}

	// Rung 1 — the authenticated gateway endpoint.
	if v, note := c.versionFromHTTP(ctx, "/api/v1/version", true); v != "" {
		out.Version, out.Via = v, VersionViaAPI
		return out
	} else if note != "" {
		out.Tried = append(out.Tried, VersionViaAPI+": "+note)
	}

	// Rung 2 — the unauthenticated root endpoint.
	if v, note := c.versionFromHTTP(ctx, "/version", false); v != "" {
		out.Version, out.Via = v, VersionViaRoot
		return out
	} else if note != "" {
		out.Tried = append(out.Tried, VersionViaRoot+": "+note)
	}

	// Rung 3 — the CLI, on whichever install kind this is.
	if v, note := c.versionFromCLI(); v != "" {
		out.Version, out.Via = v, VersionViaCLI
		return out
	} else if note != "" {
		out.Tried = append(out.Tried, VersionViaCLI+": "+note)
	}

	return out
}

// VersionProbe adapts DetectServerVersion to the (version, via, error) shape the
// headscale-version monitor wants. The error is the probe's own reason text, so
// a monitor that stores it never has to invent why the version is unknown.
func (c *Client) VersionProbe(ctx context.Context) (string, string, error) {
	sv := c.DetectServerVersion(ctx)
	if !sv.Found() {
		return "", "", errors.New(sv.Reason())
	}
	return sv.Version, sv.Via, nil
}

// versionFromHTTP runs one HTTP rung. A non-2xx answer is a NOTE, not a version:
// error bodies carry codes and ports that a loose parser would happily report as
// "the version".
func (c *Client) versionFromHTTP(ctx context.Context, path string, withAuth bool) (version, note string) {
	if c.http == nil {
		return "", "no HTTP client"
	}
	rctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return "", shortenForLog(err.Error(), 120)
	}
	if withAuth && c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", shortenForLog(err.Error(), 120)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxVersionBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Sprintf("HTTP %d %s", resp.StatusCode, shortenForLog(strings.TrimSpace(string(body)), 80))
	}
	if v := parseVersionBody(string(body)); v != "" {
		return v, ""
	}
	return "", fmt.Sprintf("HTTP %d but no version in %q", resp.StatusCode, shortenForLog(strings.TrimSpace(string(body)), 80))
}

// versionCLIRunnerFn is the test seam for rung 3. Production runs the real
// install-kind ladder; a test swaps it so the ladder's THIRD rung can be driven
// without a headscale binary or a docker daemon on the build machine (the same
// seam style as localRunner/localLookPath in local_apply_b293.go).
var versionCLIRunnerFn = func(c *Client, args ...string) ([]byte, error) {
	return c.runHeadscaleCLI(args...)
}

// versionFromCLI runs rung 3. `runHeadscaleCLI` drives exec.Command, so it has no
// context of its own: the call is bounded from the outside, because a hung
// `docker exec` must not hold a boot phase or a monitor tick. The goroutine is
// left to finish on its own and its result is discarded once the deadline has
// passed.
func (c *Client) versionFromCLI() (version, note string) {
	type cliResult struct {
		out []byte
		err error
	}
	ch := make(chan cliResult, 1)
	go func() {
		out, err := versionCLIRunnerFn(c, "version")
		ch <- cliResult{out: out, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", shortenForLog(r.err.Error(), 200)
		}
		if v := parseVersionCLI(string(r.out)); v != "" {
			return v, ""
		}
		return "", fmt.Sprintf("no version in %q", shortenForLog(strings.TrimSpace(string(r.out)), 80))
	case <-time.After(versionProbeTimeout):
		return "", fmt.Sprintf("timed out after %s", versionProbeTimeout)
	}
}

// shortenForLog collapses whitespace and truncates, so a probe note can be
// rendered in one line on a page without dragging a whole error body along.
func shortenForLog(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	if max < 1 {
		return ""
	}
	return s[:max] + "…"
}
