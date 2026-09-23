package headscale

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// B297 — the running headscale's version must be READ, not assumed.
//
// The three contracts that matter to the operator:
//  1. each spelling a real headscale answers with is understood (JSON field,
//     bare JSON string, plain text, `headscale version` output);
//  2. a non-2xx answer is NOT a version, even when its error body happens to
//     contain a version-looking number;
//  3. when every rung fails the probe says so — it never invents a version, and
//     the reason names the rungs it tried.

func TestParseVersionBody_B297(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"json version field", `{"version":"0.29.3"}`, "0.29.3"},
		{"json capitalized", `{"Version":"0.29.0"}`, "0.29.0"},
		{"json snake case", `{"server_version":"0.29.2"}`, "0.29.2"},
		{"json camel case", `{"serverVersion":"v0.29.1"}`, "0.29.1"},
		{"leading v stripped", `{"version":"v0.30.0"}`, "0.30.0"},
		{"prerelease kept", `{"version":"0.30.0-beta.1"}`, "0.30.0-beta.1"},
		{"bare json string", `"0.29.3"`, "0.29.3"},
		{"plain text", "0.29.3\n", "0.29.3"},
		{"cli-ish text", "headscale version 0.29.3", "0.29.3"},
		{"error body is not a version", `{"code":5,"message":"Not Found"}`, ""},
		{"grpc code only", `{"code":2,"message":"update is disabled"}`, ""},
		{"port is not a version", "listening on :8081", ""},
		{"empty", "", ""},
		{"commit hash", `{"version":"8eea89488c642f3d5f617fab5493d5f51f6f4ad0"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVersionBody(tc.body); got != tc.want {
				t.Fatalf("parseVersionBody(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// A long body is never loose-searched: a proxy error page or a login redirect
// can contain version-looking numbers that are not headscale's.
func TestParseVersionBody_LongBodyIsNotLooseSearched_B297(t *testing.T) {
	long := "<html><body>" + strings.Repeat("x", 400) + " nginx 1.24.0 </body></html>"
	if got := parseVersionBody(long); got != "" {
		t.Fatalf("a %d-byte body was loose-searched and produced %q", len(long), got)
	}
	// The same text, short enough to be a real answer, IS understood.
	if got := parseVersionBody("nginx 1.24.0"); got != "1.24.0" {
		t.Fatalf("short body: got %q, want 1.24.0", got)
	}
}

// `headscale version` reports the CLIENT binary too; the server line is the
// answer, because a stale /usr/bin/headscale next to a fresh container is a real
// layout.
func TestParseVersionCLI_ServerLineWins_B297(t *testing.T) {
	out := "headscale version 0.28.0\nheadscale server version: 0.29.3\n"
	if got := parseVersionCLI(out); got != "0.29.3" {
		t.Fatalf("parseVersionCLI = %q, want the server line 0.29.3", got)
	}
	if got := parseVersionCLI("headscale version 0.29.3\n"); got != "0.29.3" {
		t.Fatalf("client-only output: got %q, want 0.29.3", got)
	}
	if got := parseVersionCLI("Cannot connect to the Docker daemon\n"); got != "" {
		t.Fatalf("no version in output, got %q", got)
	}
}

func TestNormalizeServerVersion_B297(t *testing.T) {
	cases := map[string]string{
		"0.29.3":         "0.29.3",
		"v0.29.3":        "0.29.3",
		`"0.29.3"`:       "0.29.3",
		" 0.29.3 ":       "0.29.3",
		"0.29":           "0.29",
		"0.29.3+build.1": "", // not a spelling we accept — never publish it
		"latest":         "",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeServerVersion(in); got != want {
			t.Fatalf("normalizeServerVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// The ladder's ORDER is the contract: the authenticated gateway endpoint first,
// the unauthenticated root second, the CLI last.
func TestDetectServerVersion_PrefersGatewayThenRootThenCLI_B297(t *testing.T) {
	defer stubVersionCLI(t, []byte("headscale version 9.9.9\n"), nil)()

	// (a) rung 1 answers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"0.29.3"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got := New(srv.URL, "k").DetectServerVersion(context.Background())
	if got.Version != "0.29.3" || got.Via != VersionViaAPI {
		t.Fatalf("rung 1: got %q via %q, want 0.29.3 via %s", got.Version, got.Via, VersionViaAPI)
	}

	// (b) rung 1 missing, rung 2 answers plain text.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte("0.29.0\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv2.Close()
	got = New(srv2.URL, "k").DetectServerVersion(context.Background())
	if got.Version != "0.29.0" || got.Via != VersionViaRoot {
		t.Fatalf("rung 2: got %q via %q, want 0.29.0 via %s", got.Version, got.Via, VersionViaRoot)
	}
	if !strings.Contains(strings.Join(got.Tried, "; "), VersionViaAPI) {
		t.Fatalf("rung 2: the failed rung 1 must be remembered, Tried=%v", got.Tried)
	}

	// (c) both HTTP rungs missing, the CLI answers.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv3.Close()
	got = New(srv3.URL, "k").DetectServerVersion(context.Background())
	if got.Version != "9.9.9" || got.Via != VersionViaCLI {
		t.Fatalf("rung 3: got %q via %q, want 9.9.9 via %s", got.Version, got.Via, VersionViaCLI)
	}
}

// A 500 whose body contains a version must not be mistaken for the answer: the
// daemon is up but said no, and the next rung is the one that counts.
func TestDetectServerVersion_HTTPErrorBodyIsNotAVersion_B297(t *testing.T) {
	defer stubVersionCLI(t, nil, errors.New("no CLI on this build host"))()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"version":"9.9.9","message":"boom"}`))
	}))
	defer srv.Close()

	got := New(srv.URL, "k").DetectServerVersion(context.Background())
	if got.Found() {
		t.Fatalf("an error body was read as version %q via %s", got.Version, got.Via)
	}
	for _, want := range []string{VersionViaAPI, VersionViaRoot, VersionViaCLI} {
		if !strings.Contains(got.Reason(), want) {
			t.Fatalf("reason %q does not name the rung %q", got.Reason(), want)
		}
	}
	if strings.TrimSpace(got.Reason()) == "" {
		t.Fatal("a failed probe must always carry a reason")
	}
}

// An unconfigured client is not a "version 0" — it says what is missing.
func TestDetectServerVersion_NoClient_B297(t *testing.T) {
	var c *Client
	got := c.DetectServerVersion(context.Background())
	if got.Found() {
		t.Fatalf("a nil client reported version %q", got.Version)
	}
	if !strings.Contains(got.Reason(), "HEADSCALE_URL") {
		t.Fatalf("reason %q should name HEADSCALE_URL", got.Reason())
	}

	empty := New("", "")
	got = empty.DetectServerVersion(context.Background())
	if got.Found() || !strings.Contains(got.Reason(), "HEADSCALE_URL") {
		t.Fatalf("an empty BaseURL: version=%q reason=%q", got.Version, got.Reason())
	}
}

// The adapter the update monitor consumes: a found version is (version, via,
// nil); an unfound one carries the probe's own reason as the error.
func TestVersionProbeAdapter_B297(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_, _ = w.Write([]byte(`{"version":"0.29.3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	v, via, err := New(srv.URL, "k").VersionProbe(context.Background())
	if err != nil || v != "0.29.3" || via != VersionViaAPI {
		t.Fatalf("VersionProbe = (%q, %q, %v)", v, via, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer bad.Close()
	defer stubVersionCLI(t, nil, fmt.Errorf("docker exec failed"))()
	v, via, err = New(bad.URL, "k").VersionProbe(context.Background())
	if err == nil {
		t.Fatalf("a failed probe must return an error, got (%q, %q)", v, via)
	}
	if !strings.Contains(err.Error(), VersionViaAPI) {
		t.Fatalf("the error must name what was tried, got %q", err.Error())
	}
}

// stubVersionCLI replaces rung 3 for one test and restores it afterwards.
func stubVersionCLI(t *testing.T, out []byte, err error) func() {
	t.Helper()
	prev := versionCLIRunnerFn
	versionCLIRunnerFn = func(_ *Client, _ ...string) ([]byte, error) { return out, err }
	return func() { versionCLIRunnerFn = prev }
}
