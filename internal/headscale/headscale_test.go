package headscale

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseDuration_Formats(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1h", time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		// RFC3339 in the future
		{"2030-01-01T00:00:00Z", 0, false}, // exact value depends on test time, just check no err
		// garbage
		{"yesterday", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseDuration(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseDuration(%q) err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if c.err {
			continue
		}
		// for RFC3339 input, just assert >= 0 (future relative to test now)
		if c.in == "2030-01-01T00:00:00Z" {
			// it's a fixed future timestamp; check sign/gross size
			if got < 0 || got > 100*365*24*time.Hour {
				t.Errorf("parseDuration(%q) = %v; out of plausible range", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("parseDuration(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestDurationFlag_Formats(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{24 * time.Hour, "24h"},
		{30 * time.Minute, "30m"},
		{5 * time.Minute, "5m"},
		{time.Minute, "1m"},
		{45 * time.Second, "45s"},
		{0, "0s"},
		// 1h30m falls into the minutes branch (90m). That's the documented
		// behavior of durationFlag — verify it stays stable.
		{90 * time.Minute, "90m"},
	}
	for _, c := range cases {
		got := durationFlag(c.in)
		if got != c.want {
			t.Errorf("durationFlag(%v) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestHasExitNodeTag(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		routes []string
		want  bool
	}{
		{"empty node", nil, nil, false},
		// explicit tag
		{"tag:exit-node", []string{"tag:exit-node"}, nil, true},
		{"tag:exit-node uppercase", []string{"TAG:EXIT-NODE"}, nil, true},
		{"other tag", []string{"tag:something-else"}, nil, false},
		// name-based
		{"exit-karolina", nil, nil, true},
		{"exitnode-foo", nil, nil, true},
		{"EXIT-Bar", nil, nil, true},
		{"not-exit-baz", nil, nil, false},
		// route-based (0.29.1 detection)
		{"advertises 0.0.0.0/0", nil, []string{"0.0.0.0/0"}, true},
		{"advertises ::/0", nil, []string{"::/0"}, true},
		{"advertises only 10.0.0.0/8", nil, []string{"10.0.0.0/8"}, false},
	}
	for _, c := range cases {
		got := hasExitNodeTag(c.tags, c.name, c.routes)
		if got != c.want {
			t.Errorf("hasExitNodeTag(name=%q tags=%v routes=%v) = %v want %v",
				c.name, c.tags, c.routes, got, c.want)
		}
	}
}

func TestIsPublicAndPrivate(t *testing.T) {
	pub := NodeView{Tags: []string{"tag:public"}}
	if !pub.IsPublicView() {
		t.Error("tag:public should be IsPublicView")
	}
	if pub.IsPrivateView() {
		t.Error("tag:public should NOT be IsPrivateView")
	}
	priv := NodeView{Tags: []string{"tag:private"}}
	if priv.IsPublicView() {
		t.Error("tag:private should NOT be IsPublicView")
	}
	if !priv.IsPrivateView() {
		t.Error("tag:private should be IsPrivateView")
	}
	none := NodeView{Tags: []string{"tag:other"}}
	if none.IsPublicView() || none.IsPrivateView() {
		t.Error("unrelated tag should fail both")
	}
	// case-insensitive
	upPub := NodeView{Tags: []string{"TAG:Public"}}
	if !upPub.IsPublicView() {
		t.Error("uppercase TAG:Public should still match (EqualFold)")
	}
}

func TestGetenvDefault(t *testing.T) {
	t.Setenv("XHS_TEST_KEY", "real")
	if got := getenvDefault("XHS_TEST_KEY", "default"); got != "real" {
		t.Errorf("got %q want real", got)
	}
	if got := getenvDefault("XHS_MISSING_KEY", "default"); got != "default" {
		t.Errorf("got %q want default", got)
	}
	t.Setenv("XHS_EMPTY_KEY", "")
	if got := getenvDefault("XHS_EMPTY_KEY", "default"); got != "default" {
		t.Errorf("empty env should fall back to default, got %q", got)
	}
}

// HSNode.IsPublic has identical semantics to NodeView.IsPublicView — sanity check.
func TestHSNodeIsPublic(t *testing.T) {
	n := HSNode{Tags: []string{"tag:public"}}
	if !n.IsPublic() {
		t.Error("HSNode with tag:public should be IsPublic")
	}
}

// --- 2026-07-13: APIError + SetPolicy status-code gating ---
//
// SetPolicy used to fall back to file-mode on ANY non-2xx response,
// which silently masked transient headscale 5xx errors. The fix gates
// the fallback on 404/405 only and propagates 5xx via *APIError.
// These tests pin that contract.

// TestAPIErrorMessage pins the legacy "headscale METHOD PATH: CODE BODY"
// format so log scrapers / greps stay readable.
func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Method: "PUT", Path: "/api/v1/policy", StatusCode: 500, Body: "policy boom"}
	got := e.Error()
	want := "headscale PUT /api/v1/policy: 500 policy boom"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// fakeHS is a minimal httptest server that responds to PUT
// /api/v1/policy with the given status + body. Other methods/paths
// return 404 so unexpected calls are visible.
func fakeHS(t *testing.T, status int, body string) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/policy" && r.Method == http.MethodPut {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "fake-key")
}

// TestSetPolicyPropagates5xx verifies that a 500 from headscale is
// returned as *APIError and that the file-mode docker fallback is NOT
// attempted. The previous behaviour masked 5xx by writing a hujson
// file via docker run (which succeeded on the deployment VM because
// docker is available there).
func TestSetPolicyPropagates5xx(t *testing.T) {
	_, c := fakeHS(t, http.StatusInternalServerError, "policy boom")
	err := c.SetPolicy(`{"acls":[]}`)
	if err == nil {
		t.Fatal("SetPolicy returned nil; expected *APIError for 500")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err is not *APIError: %T %v", err, err)
	}
	if apiErr.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if apiErr.Body != "policy boom" {
		t.Errorf("Body = %q, want %q", apiErr.Body, "policy boom")
	}
}

// TestSetPolicy404TriggersFileModeFallback documents the contract:
// 404 from headscale is the signal for "policy endpoint not available
// in this mode" and the fallback fires. We can't observe the docker
// write itself in a unit test (no docker in CI), but we CAN observe
// that the returned error is NOT the *APIError — the fallback
// either succeeds (returning nil) or wraps the docker failure.
// Either way, the raw APIError must not be returned to the caller.
func TestSetPolicy404NotPropagatedAsAPIError(t *testing.T) {
	_, c := fakeHS(t, http.StatusNotFound, "policy not in database mode")
	err := c.SetPolicy(`{"acls":[]}`)
	if err == nil {
		// docker not available in CI → fallback returns wrapped error.
		// On a deployment VM with docker, this would also be nil
		// (fallback succeeds).
		return
	}
	// The fallback path on a CI machine fails to run `docker` and
	// returns fmt.Errorf("api: %v; write acl file: %v", err, cerr).
	// The wrapped *APIError is still extractable via errors.As, but
	// the outer error MUST be a non-nil combined message — proving
	// the fallback path was taken (not the "return apiErr early" path).
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected wrapped APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("wrapped StatusCode = %d, want 404", apiErr.StatusCode)
	}
	// The outer error message should mention the fallback write failure.
	if msg := err.Error(); !contains(msg, "write acl file") && !contains(msg, "restart") {
		t.Errorf("error message %q does not look like a file-mode fallback failure", msg)
	}
}

// contains is a tiny strings.Contains shim so we don't pull in the
// strings package just for two substring checks.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
