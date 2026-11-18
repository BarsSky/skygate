// check_endpoint_b491_test.go — TD-39: the check must have more than one door.
//
// The measured case these tests encode: api.github.com times out from the production host (DPI) while
// another endpoint answers, and the operator must be able to say WHICH endpoints to try — in order,
// with the git-tags door still last. Each test names the trap it prevents.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseCheckEndpoints_B491 — the parser is the only thing between the operator's text and an HTTP
// request, so it must accept both separators, keep the PRIORITY order, dedupe, and refuse a non-http
// scheme (a file:// endpoint would be a local file read dressed up as a version check).
func TestParseCheckEndpoints_B491(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{"empty is the default", "   ", nil, false},
		{"single", "https://mirror.example.com/latest.json", []string{"https://mirror.example.com/latest.json"}, false},
		{"comma and space both work", "https://a.example/1, https://b.example/2 https://c.example/3",
			[]string{"https://a.example/1", "https://b.example/2", "https://c.example/3"}, false},
		{"order is the priority and is preserved", "https://b.example/2\nhttps://a.example/1",
			[]string{"https://b.example/2", "https://a.example/1"}, false},
		{"duplicates are dropped once", "https://a.example/1,https://a.example/1",
			[]string{"https://a.example/1"}, false},
		{"http is allowed (a LAN mirror)", "http://192.0.2.10:8080/latest.json",
			[]string{"http://192.0.2.10:8080/latest.json"}, false},
		{"file scheme is refused", "file:///etc/passwd", nil, true},
		{"anything else is refused", "gopher://example.com/x", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCheckEndpoints(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckEndpointsForPrecedence_B491 — env beats the DB setting, the DB setting beats the default,
// and an EMPTY pair still yields exactly one usable endpoint (so the caller never special-cases "").
func TestCheckEndpointsForPrecedence_B491(t *testing.T) {
	got, err := CheckEndpointsFor("https://env.example/latest.json", "https://db.example/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "https://env.example/latest.json" {
		t.Fatalf("env did not win: %v", got)
	}

	got, err = CheckEndpointsFor("", "https://db.example/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "https://db.example/latest.json" {
		t.Fatalf("db setting did not win over the default: %v", got)
	}

	got, err = CheckEndpointsFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != DefaultCheckEndpoint {
		t.Fatalf("empty config did not fall back to the default: %v", got)
	}
}

// TestEndpointURLsExpandPlaceholders_B491 — one configured template follows a fork: {owner}/{repo}
// are substituted, and a list that expands to nothing still yields one URL.
func TestEndpointURLsExpandPlaceholders_B491(t *testing.T) {
	c := &Checker{Owner: "acme", Repo: "skygate",
		Endpoints: []string{"https://mirror.example.com/{owner}/{repo}/latest.json"}}
	urls := c.endpointURLs()
	if len(urls) != 1 || urls[0] != "https://mirror.example.com/acme/skygate/latest.json" {
		t.Fatalf("placeholders not expanded: %v", urls)
	}

	empty := &Checker{Owner: "acme", Repo: "skygate", Endpoints: []string{"", "  "}}
	urls = empty.endpointURLs()
	if len(urls) != 1 || !strings.Contains(urls[0], "api.github.com") {
		t.Fatalf("an all-empty list must still produce the default endpoint, got %v", urls)
	}
}

// TestFetchFallsThroughEndpoints_B491 — THE case TD-39 exists for: the first endpoint is unreachable,
// the second answers, and the result is reported as a MIRROR answer (not as a GitHub one).
func TestFetchFallsThroughEndpoints_B491(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no route to host", http.StatusBadGateway)
	}))
	defer dead.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "v9.9.9", HTMLURL: "https://example.com/r/v9.9.9"})
	}))
	defer mirror.Close()

	c := &Checker{
		HTTPClient:     defaultCheckerClient(),
		Owner:          "acme",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "1.0.0",
		Endpoints:      []string{dead.URL + "/latest.json", mirror.URL + "/latest.json"},
	}
	res, err := c.fetch(context.Background())
	if err != nil {
		t.Fatalf("the second endpoint should have answered: %v", err)
	}
	if res.Latest != "v9.9.9" || !res.IsNewer {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Source != SourceMirror {
		t.Fatalf("a mirror answer must be labelled %q, got %q (a page that cannot tell GitHub from a mirror hides a stale mirror)", SourceMirror, res.Source)
	}
}

// TestFetchSendsTokenOnlyToGitHub_B491 — a GitHub token sent to a MIRROR would hand the credential to
// whoever runs it. The repository is public, so the check must work without it there.
func TestFetchSendsTokenOnlyToGitHub_B491(t *testing.T) {
	var sawAuth string
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "v2.0.0"})
	}))
	defer mirror.Close()

	c := &Checker{
		HTTPClient:     defaultCheckerClient(),
		Owner:          "acme",
		Repo:           "skygate",
		Channel:        "stable",
		GitHubToken:    "ghp_secret_value",
		CurrentVersion: "1.0.0",
		Endpoints:      []string{mirror.URL + "/latest.json"},
	}
	if _, err := c.fetch(context.Background()); err != nil {
		t.Fatalf("mirror should have answered: %v", err)
	}
	if sawAuth != "" {
		t.Fatalf("the GitHub token was sent to a mirror (%q) — that is a credential leak", sawAuth)
	}
	if !isGitHubAPIHost("https://api.github.com/repos/x/y/releases/latest") || isGitHubAPIHost(mirror.URL+"/latest.json") {
		t.Fatalf("isGitHubAPIHost misclassifies an endpoint")
	}
}

// TestFetchFallsBackToTagsWhenAllEndpointsFail_B491 — the B445 door must stay reachable: when every
// configured endpoint is down, the version still comes from the repository's tags, with the notes
// marked unavailable rather than invented.
func TestFetchFallsBackToTagsWhenAllEndpointsFail_B491(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unreachable", http.StatusGatewayTimeout)
	}))
	defer dead.Close()

	c := &Checker{
		HTTPClient:     defaultCheckerClient(),
		Owner:          "acme",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "1.0.0",
		Endpoints:      []string{dead.URL + "/latest.json"},
		TagLister: func(ctx context.Context) ([]string, error) {
			return []string{"refs/tags/v1.5.236"}, nil
		},
	}
	res, err := c.fetch(context.Background())
	if err != nil {
		t.Fatalf("the tag fallback should have answered: %v", err)
	}
	if res.Latest != "v1.5.236" || res.Source != SourceGitTags {
		t.Fatalf("unexpected fallback result: %+v", res)
	}
	if !res.NotesUnavailable {
		t.Fatalf("the fallback must mark the notes unavailable (it never read them)")
	}
	if !strings.Contains(res.NotesReason, "unreachable") && !strings.Contains(res.NotesReason, "504") {
		t.Fatalf("the fallback must carry the reason the primary doors failed, got %q", res.NotesReason)
	}
}

// TestAllEndpointsFailReturnsTheFirstError_B491 — the message the page shows must name an endpoint,
// not "some error": the whole B486 complaint was that the message told nobody where to look.
func TestAllEndpointsFailReturnsTheFirstError_B491(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer dead.Close()

	c := &Checker{HTTPClient: defaultCheckerClient(), Channel: "stable",
		Endpoints: []string{dead.URL + "/first.json", dead.URL + "/second.json"}}
	_, err := c.fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error when every endpoint fails")
	}
	if !strings.Contains(err.Error(), "first.json") {
		t.Fatalf("the error must name the endpoint that was tried first, got: %v", err)
	}
	_ = fmt.Sprint() // keep fmt imported for the table above
}
