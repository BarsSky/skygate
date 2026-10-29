// tagtruth_b445_test.go — B445. Two doors to the same answer, and the second one must be
// exact about what it does NOT know.
package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestB445_HighestSemverTagIgnoresJunkAndDuplicates pins the ref parsing against a REAL
// `git ls-remote --tags --refs` listing: annotated tags appear twice (once peeled), and a
// repository accumulates hand-made tags that are not versions at all.
func TestB445_HighestSemverTagIgnoresJunkAndDuplicates(t *testing.T) {
	refs := []string{
		"aaa refs/tags/v1.5.189",
		"bbb refs/tags/v1.5.190",
		"ccc refs/tags/v1.5.190^{}",
		"ddd refs/tags/v1.5.195",
		"eee refs/tags/backup-2026-10-29",
		"fff refs/tags/v1.5",
		"ggg refs/tags/v2.0.0-rc.1",
		"hhh refs/tags/v1.4.9",
	}
	tag, ver, ok := HighestSemverTag(refs, "stable")
	if !ok {
		t.Fatalf("no tag chosen out of %v", refs)
	}
	if tag != "v1.5.195" || ver != "1.5.195" {
		t.Fatalf("chosen %q (%q), want v1.5.195 — a stable channel must skip the rc and every non-version", tag, ver)
	}
	// The channel knob is the same one the API path honours.
	if tag, _, ok := HighestSemverTag(refs, "all"); !ok || tag != "v2.0.0-rc.1" {
		t.Fatalf("channel=all chose %q, want v2.0.0-rc.1", tag)
	}
}

// TestB445_HighestSemverTagOnAnEmptyList: a repo with no tags (or a failed listing) must not
// invent a version.
func TestB445_HighestSemverTagOnAnEmptyList(t *testing.T) {
	if _, _, ok := HighestSemverTag(nil, "stable"); ok {
		t.Fatal("an empty ref list produced a tag")
	}
	if _, _, ok := HighestSemverTag([]string{"aaa refs/tags/main", "bbb refs/heads/dev"}, "stable"); ok {
		t.Fatal("branches were treated as versions")
	}
}

// TestB445_RateLimitedAPIStillAnswersFromTags is the live shape: the API answers 403 (quota
// exhausted) and the page must STILL show the newest version — read from the tags — while
// being explicit that the notes are missing.
func TestB445_RateLimitedAPIStillAnswersFromTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "1793306619")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Checker{
		HTTPClient:     srv.Client(),
		Owner:          "BarsSky",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "v1.5.189+9196748d",
		TagLister: func(ctx context.Context) ([]string, error) {
			return []string{"a refs/tags/v1.5.194", "b refs/tags/v1.5.195"}, nil
		},
		ReleaseURLTemplate: func(tag string) string { return "https://example.test/" + tag },
	}
	// Point the API call at the stub server: the production URL is hardcoded, so replace
	// the transport instead of the URL (httptest + rewriteTransport, the package's usual
	// pattern).
	c.HTTPClient = &http.Client{Transport: rewriteTransport{target: srv.URL}}

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check returned an error although the fallback should answer: %v", err)
	}
	if res.Latest != "v1.5.195" {
		t.Fatalf("Latest = %q, want v1.5.195 from the tags", res.Latest)
	}
	if !res.IsNewer {
		t.Error("v1.5.195 over v1.5.189 must be NEWER — the page has to show an installable update")
	}
	if res.Source != SourceGitTags {
		t.Errorf("Source = %q, want %q", res.Source, SourceGitTags)
	}
	if !res.NotesUnavailable {
		t.Error("NotesUnavailable must be true — the notes were not fetched")
	}
	if !strings.Contains(res.NotesReason, "rate limited") {
		t.Errorf("NotesReason = %q, want the API failure named", res.NotesReason)
	}
	// The page must not be told "check failed" when it has a usable answer.
	if res.Error != "" {
		t.Errorf("Error = %q — a fallback success must not set Error, or every render bypasses the cache", res.Error)
	}
}

// TestB445_RateLimitedAPIWithoutTagsIsStillAnError: with no second door there is no answer,
// and the error must name the remedy.
func TestB445_RateLimitedAPIWithoutTagsIsStillAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "1793306619")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Checker{
		Owner:          "BarsSky",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "v1.5.189",
		TagLister: func(ctx context.Context) ([]string, error) {
			return nil, errors.New("git: not a repository")
		},
	}
	c.HTTPClient = &http.Client{Transport: rewriteTransport{target: srv.URL}}
	res, err := c.Check(context.Background())
	if err == nil && res != nil && res.Latest != "" {
		t.Fatalf("an unusable API plus an unusable fallback reported a version: %+v", res)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	} else if res != nil {
		msg = res.Error
	}
	if !strings.Contains(msg, "rate limited") || !strings.Contains(msg, "SKYGATE_GITHUB_TOKEN") {
		t.Fatalf("error %q must name the rate limit AND the remedy (SKYGATE_GITHUB_TOKEN)", msg)
	}
	// The reset time is reported in WORDS, not only as unix seconds.
	if !strings.Contains(msg, "resets at") {
		t.Errorf("error %q does not say WHEN it will be retried", msg)
	}
}

// TestB445_FallbackIsCachedLikeASuccess: the fallback must not turn every page render into a
// `git ls-remote` — that is the quota-burning shape B424 fixed for the API, one level down.
func TestB445_FallbackIsCachedLikeASuccess(t *testing.T) {
	calls := 0
	c := &Checker{
		Owner: "x", Repo: "y", Channel: "stable", CurrentVersion: "v1.0.0",
		HTTPClient: &http.Client{Transport: rewriteTransport{target: "http://127.0.0.1:1"}},
		TagLister: func(ctx context.Context) ([]string, error) {
			calls++
			return []string{"a refs/tags/v1.2.0"}, nil
		},
	}
	// The API call fails (nothing listens on port 1), so the first Check goes to the tags.
	if res, _ := c.Check(context.Background()); res == nil || res.Latest != "v1.2.0" {
		t.Fatalf("first check = %+v", res)
	}
	for i := 0; i < 3; i++ {
		_, _ = c.Check(context.Background())
	}
	if calls != 1 {
		t.Fatalf("the tag listing ran %d times — a cached fallback answer must be served from memory", calls)
	}
}
