package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.28.6", "0.28.6", 0},
		{"0.28.6", "0.28.7", -1},
		{"0.28.7", "0.28.6", 1},
		{"0.29.0", "0.28.6", 1},
		{"v0.29.0", "v0.28.6", 1},
		// Build label suffix is stripped
		{"0.28.6+abc1234", "0.28.6", 0},
		{"0.28.6+abc1234", "0.28.7+def5678", -1},
		// Padding
		{"0.29", "0.28.6", 1},
		{"0.28.6", "0.29", -1},
		// Major bumps
		{"1.0.0", "0.99.99", 1},
		// B124: git-describe "-N-g<hex>" suffix is stripped.
		// Before B124, these mis-compares gave WRONG results
		// (e.g. "1.3.9" > "1.3.11-27-g03a1d97" because of the
		// lex fallback on the "11-27-g..." part). After B124,
		// "1.3.11-27-g03a1d97" compares equal to "1.3.11" — the
		// build is on the v1.3.11 line, just ahead of the tag.
		{"1.3.11-27-g03a1d97", "1.3.11", 0},
		{"1.3.11-27-g03a1d97", "1.3.9", 1},  // local ahead
		{"1.3.9", "1.3.11-27-g03a1d97", -1}, // GitHub old, local new
		{"1.3.11-1-gabc1234", "1.3.11", 0},
		{"1.3.11-0-gdeadbeef", "1.3.12", -1},
		// Build label with both git-describe and explicit commit
		{"1.3.11-27-g03a1d97+extra", "1.3.11", 0},
		// 4-component version (skygate v1.3.12+ convention):
		// "1.3.19.2" compares on all 4 parts (B128). The
		// pre-B128 code used `splitVersionParts(a, 3)` which
		// DROPPED the 4th component — so v1.3.19.2 == v1.3.19.4
		// in the comparison, and the live "Update" button on
		// /admin/update stayed hidden because IsNewer returned
		// false. Before B124 split, the 3-part SplitN gave
		// ["1","3","19.2"] and the parseUint fallback lex-compared
		// "9" < "19.2" (incorrectly reporting v1.3.9 as newer
		// than v1.3.19.2).
		{"1.3.19.2", "1.3.9", 1},  // local newer (live B124 case)
		{"1.3.9", "1.3.19.2", -1}, // GitHub old, local new
		{"1.3.19.2", "1.3.19.2", 0},
		// B128: 4th component IS now compared.
		{"1.3.19.2", "1.3.19.1", 1}, // sub-patch bump
		{"1.3.19.1", "1.3.19.2", -1},
		{"1.3.19.4", "1.3.19.2", 1},  // the live B128 trigger
		{"1.3.19.2", "1.3.19.4", -1}, // the live B128 trigger (reversed)
		// Padding still works for 3-part versions.
		{"1.3.19", "1.3.19.0", 0}, // implicit .0 == explicit .0
		{"1.3.19.1", "1.3.19", 1}, // .1 > implicit .0
		// (Pre-release suffix handling like "0.29.0-rc.1" is a
		// separate concern — the channel check filters those
		// out before compareSemver is called. compareSemver
		// itself doesn't claim to handle pre-release ordering.)
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_vs_%s", c.a, c.b), func(t *testing.T) {
			got := compareSemver(c.a, c.b)
			if got != c.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestStripBuildLabelSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1.3.11-27-g03a1d97", "1.3.11"},
		{"1.3.11-1-gabc1234", "1.3.11"},
		{"1.3.11-0-gdeadbeef", "1.3.11"},
		{"1.3.11-27-g03a1d97+extra", "1.3.11"},
		{"0.28.6+abc1234", "0.28.6"},
		{"1.3.11", "1.3.11"},               // unchanged
		{"v1.3.11-27-g03a1d97", "v1.3.11"}, // preserves "v"
		// Plain pre-release NOT stripped
		{"0.29.0-rc.1", "0.29.0-rc.1"},
		{"0.29.0-beta", "0.29.0-beta"},
		// Numeric "-N" is not a git-describe (no -g<hex> part)
		{"0.29.0-1", "0.29.0-1"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := stripBuildLabelSuffix(c.in)
			if got != c.want {
				t.Errorf("stripBuildLabelSuffix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestHasPrereleaseSuffix(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.29.0", false},
		{"0.29.0-rc.1", true},
		{"0.29.0-beta", true},
		{"0.29.0-pre+abc1234", true},
		{"0.29.0-1", false}, // "-1" is numeric, treated as not-prerelease
		{"0.29.0+abc1234", false},
		{"1.0.0-alpha.1", true},
	}
	for _, c := range cases {
		t.Run(c.v, func(t *testing.T) {
			got := hasPrereleaseSuffix(c.v)
			if got != c.want {
				t.Errorf("hasPrereleaseSuffix(%q) = %v, want %v", c.v, got, c.want)
			}
		})
	}
}

func TestChecker_Fetch_Success(t *testing.T) {
	release := ghRelease{
		TagName:     "v0.29.0",
		Name:        "v0.29.0",
		HTMLURL:     "https://github.com/skygate-operator/skygate/releases/tag/v0.29.0",
		Body:        "## v0.29.0\n\nSelf-update mechanism + PG migration hardening.",
		PublishedAt: time.Now().Add(-1 * time.Hour),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitHub adds the X-RateLimit-* headers even on success
		w.Header().Set("X-RateLimit-Remaining", "59")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	// We can't easily override the GitHub URL, so we
	// patch the Owner/Repo to point at a path the
	// testserver will accept — but the testserver URL
	// is hardcoded. So we test the lower-level parsing
	// path: feed a fake HTTPClient that points to the
	// testserver via a URL rewrite.

	// B431: the transport now really redirects api.github.com → srv.URL, so this test
	// asserts the parsed release instead of logging that the redirect does not work.
	c := &Checker{
		HTTPClient:     &http.Client{Transport: rewriteTransport{target: srv.URL}},
		Owner:          "TestOwner",
		Repo:           "TestRepo",
		Channel:        "stable",
		CurrentVersion: "0.28.6",
	}
	result, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check through a redirected transport: %v", err)
	}
	if result == nil {
		t.Fatal("Check returned a nil Result")
	}
	if result.Error != "" {
		t.Fatalf("Check reported Error = %q on a healthy stand-in", result.Error)
	}
	if result.Latest != "v0.29.0" {
		t.Fatalf("Latest = %q, want v0.29.0", result.Latest)
	}
	if !result.IsNewer {
		t.Fatalf("IsNewer = false for 0.28.6 → v0.29.0")
	}
	if result.CheckedAt.IsZero() {
		t.Fatal("CheckedAt is zero — the page would render an empty «Проверено»")
	}
}

// rewriteTransport rewrites every request to point at target.
// Used by tests to redirect api.github.com → httptest server.
//
// B431: this helper never actually worked. Its old body split the target on "://" and
// assigned the remainder (including the separator) to URL.Host, so every redirected
// request failed with `dial tcp: lookup ://127.0.0.1: no such host` — which is exactly
// what the log line in TestChecker_Fetch_Success said ("expected: fetch fails with no
// rewrite; we need a real rewrite transport") while the test asserted nothing. The
// redirect is now built from net/url, and that test asserts the parsed release.
type rewriteTransport struct {
	target string
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.target)
	if err != nil {
		return nil, fmt.Errorf("rewriteTransport: parse %q: %w", r.target, err)
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = u.Scheme
	clone.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestChecker_Fetch_StripsTokenFromLogs(t *testing.T) {
	// SanitizeAPIURL should be a no-op for our case (we use
	// Bearer auth, not query string) but exist for future-proofing.
	got := SanitizeAPIURL("https://api.github.com/repos/foo/bar/releases/latest?access_token=secret")
	if strings.Contains(got, "secret") {
		t.Errorf("SanitizeAPIURL leaked token: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	short := "hello"
	if truncate(short, 100) != short {
		t.Errorf("truncate should not change short strings, got %q", truncate(short, 100))
	}
	long := strings.Repeat("x", 500)
	out := truncate(long, 100)
	if len(out) > 200 {
		t.Errorf("truncate should cap output, got %d chars", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncate should mark truncation, got %q", out)
	}
}

func TestChecker_Check_BackOffOnFailure(t *testing.T) {
	// A checker that has no lastResult and just had a failure
	// should back off (return a Result with Error set, no
	// GitHub call) on the next Check().
	c := &Checker{
		HTTPClient:     &http.Client{},
		Owner:          "BarsSky",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "0.28.6",
	}
	c.lastErrorAt = time.Now()
	c.lastResult = nil
	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Error == "" {
		t.Error("expected Error to be set during back-off, got empty")
	}
	if !strings.Contains(res.Error, "backing off") {
		t.Errorf("expected back-off message, got %q", res.Error)
	}
}

func TestParseUint(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"0", 0, false},
		{"1", 1, false},
		{"99", 99, false},
		{"0.5", 0, true}, // not pure digits
		{"abc", 0, true},
		{"", 0, true},
		{"1234567890", 1234567890, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseUint(c.in)
			if c.err {
				if err == nil {
					t.Errorf("parseUint(%q): expected error, got %d", c.in, got)
				}
			} else if got != c.want {
				t.Errorf("parseUint(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
