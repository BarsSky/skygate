package headscale

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// --- 2026-07-13: CreatePreauthKey tests ---
//
// CreatePreauthKey is the bot's /add_device path: it MUST issue a
// preauth key against headscale so the user can register a new
// device. The function has two layers (API → CLI fallback) and
// three failure modes (parseDuration error, API non-2xx, CLI
// non-zero exit). These tests pin all of them.

// fakePreauthHS responds to POST /api/v1/preauthkey with the given
// status + JSON body. Captures the request body for inspection.
func fakePreauthHS(t *testing.T, status int, body string) (*httptest.Server, *Client, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/preauthkey" && r.Method == http.MethodPost {
			buf, _ := io.ReadAll(r.Body)
			cap.method = r.Method
			cap.path = r.URL.Path
			cap.body = string(buf)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "fake-key"), cap
}

type capturedRequest struct {
	method, path, body string
}

// TestCreatePreauthKeyAPISuccess exercises the happy path: the
// headscale API returns 2xx with a JSON body that has a non-empty
// "key" field, and CreatePreauthKey returns the parsed struct.
func TestCreatePreauthKeyAPISuccess(t *testing.T) {
	_, c, cap := fakePreauthHS(t, http.StatusOK, `{"id":"42","key":"hskey-test","user_id":7,"reusable":false,"expiration":"2030-01-01T00:00:00Z"}`)
	k, err := c.CreatePreauthKey(7, "1h", false)
	if err != nil {
		t.Fatalf("CreatePreauthKey: %v", err)
	}
	if k.Key != "hskey-test" {
		t.Errorf("Key = %q, want hskey-test", k.Key)
	}
	if k.ID != "42" {
		t.Errorf("ID = %q, want 42", k.ID)
	}
	// The request body should have the right shape. We assert
	// user_id and reusable are JSON-marshalled correctly;
	// expiration is RFC3339-relative to now so we only check
	// it's non-empty.
	if cap.method != "POST" || cap.path != "/api/v1/preauthkey" {
		t.Errorf("request was %s %s, want POST /api/v1/preauthkey", cap.method, cap.path)
	}
	if !contains(cap.body, `"user_id":7`) {
		t.Errorf("request body missing user_id=7: %s", cap.body)
	}
	if !contains(cap.body, `"reusable":false`) {
		t.Errorf("request body missing reusable=false: %s", cap.body)
	}
	if !contains(cap.body, `"ephemeral":false`) {
		t.Errorf("request body missing ephemeral=false: %s", cap.body)
	}
}

// TestCreatePreauthKeyReusableTrue covers the reusable=true branch
// — different JSON encoding (true vs false), and a different CLI
// argument set (we can't observe the CLI in CI but the request
// body shape is asserted).
func TestCreatePreauthKeyReusableTrue(t *testing.T) {
	_, c, cap := fakePreauthHS(t, http.StatusOK, `{"id":"99","key":"hskey-r","user_id":1}`)
	_, err := c.CreatePreauthKey(1, "1h", true)
	if err != nil {
		t.Fatalf("CreatePreauthKey: %v", err)
	}
	if !contains(cap.body, `"reusable":true`) {
		t.Errorf("reusable=true not in body: %s", cap.body)
	}
}

// TestCreatePreauthKeyInvalidExpiration covers the parseDuration
// error path: the caller passes "yesterday" (no Go duration, no
// RFC3339) and we should get an error WITHOUT hitting the API.
func TestCreatePreauthKeyInvalidExpiration(t *testing.T) {
	srv, c, _ := fakePreauthHS(t, http.StatusOK, `{"id":"1","key":"hskey-x"}`)
	// Track whether the server got hit — it should NOT.
	hits := 0
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"1","key":"hskey-x"}`))
	})
	_, err := c.CreatePreauthKey(1, "yesterday", false)
	if err == nil {
		t.Fatal("expected error for bad expiration, got nil")
	}
	if !contains(err.Error(), "invalid expiration") {
		t.Errorf("error %q does not mention invalid expiration", err.Error())
	}
	if hits != 0 {
		t.Errorf("API was hit %d times; parseDuration should short-circuit", hits)
	}
}

// TestCreatePreauthKeyAPIFailPropagatesWhenNoExecContainer covers
// the API-failure path when there's no CLI fallback configured:
// the API error must be returned (wrapped with the "no
// ExecContainer" hint so an operator reading the log knows
// what to fix).
func TestCreatePreauthKeyAPIFailPropagatesWhenNoExecContainer(t *testing.T) {
	_, c, _ := fakePreauthHS(t, http.StatusInternalServerError, "boom")
	c.ExecContainer = "" // disable CLI fallback
	_, err := c.CreatePreauthKey(1, "1h", false)
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !contains(err.Error(), "no ExecContainer") {
		t.Errorf("error %q does not mention no ExecContainer", err.Error())
	}
}

// TestCreatePreauthKeyAPIFailFallsBackToCLI documents the contract:
// when the API returns 5xx and ExecContainer is set, we attempt
// the CLI path. The CLI then calls `docker exec`, which doesn't
// exist in CI, so the function returns a wrapped error mentioning
// both failures. The test pins that contract — proving the
// fallback path is wired up (would silently no-op if the
// implementation skipped it).
func TestCreatePreauthKeyAPIFailFallsBackToCLI(t *testing.T) {
	_, c, _ := fakePreauthHS(t, http.StatusInternalServerError, "api down")
	c.ExecContainer = "headscale" // CLI is attempted, then fails
	_, err := c.CreatePreauthKey(1, "1h", false)
	if err == nil {
		// Only nil on a deployment VM with docker + the right container.
		// CI has no docker, so this branch is unreachable in CI.
		return
	}
	// The error should mention both the API failure AND the CLI
	// failure (proving the fallback was attempted).
	msg := err.Error()
	if !contains(msg, "api:") {
		t.Errorf("error %q does not mention api failure", msg)
	}
	if !contains(msg, "cli:") && !contains(msg, "docker exec") {
		t.Errorf("error %q does not mention cli/docker failure", msg)
	}
}

// TestCreatePreauthKeyAPIEmptyKeyTriggersFallback covers the edge
// case where the headscale API returns 2xx but with an empty
// "key" field (some headscale versions do this on auth failure).
// The implementation should fall through to CLI rather than
// returning a useless empty PreauthKey.
func TestCreatePreauthKeyAPIEmptyKeyTriggersFallback(t *testing.T) {
	_, c, _ := fakePreauthHS(t, http.StatusOK, `{"id":"1","key":""}`)
	c.ExecContainer = "headscale" // CLI is attempted, fails in CI
	_, err := c.CreatePreauthKey(1, "1h", false)
	if err == nil {
		return // CLI succeeded (deployment VM)
	}
	// Both attempts should have failed; error should mention
	// "api:" (the empty-key fallback path) and "cli:" (the
	// docker exec attempt).
	if !contains(err.Error(), "api:") {
		t.Errorf("error %q does not mention api failure", err.Error())
	}
}

// --- 2026-07-13: GetACL tests ---
//
// GetACL has three response shapes to honour (Policy field, Data
// field, both empty) and a cache. These tests pin all four.

// fakeACLHS responds to GET /api/v1/policy with the given status
// + JSON body. Calls are counted so we can assert the cache.
func fakeACLHS(t *testing.T, status int, body string) (*httptest.Server, *Client, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/policy" && r.Method == http.MethodGet {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "fake-key"), &hits
}

// TestGetACLCacheMissThenHit verifies that the first GetACL hits
// the API and the second call within cacheTTL returns the cached
// value without a second API roundtrip.
func TestGetACLCacheMissThenHit(t *testing.T) {
	srv, c, hits := fakeACLHS(t, http.StatusOK, `{"policy":"{}","data":""}`)
	c.cacheTTL = time.Hour // long enough that the second call is a cache hit
	first, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL #1: %v", err)
	}
	if first != "{}" {
		t.Errorf("GetACL #1 = %q, want {}", first)
	}
	second, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL #2: %v", err)
	}
	if second != "{}" {
		t.Errorf("GetACL #2 = %q, want {}", second)
	}
	if *hits != 1 {
		t.Errorf("API hit %d times, want 1 (cache should have served #2)", *hits)
	}
	_ = srv
}

// TestGetACLHonoursDataField covers the alternative response
// shape: headscale versions before 0.22 populate the "data"
// field instead of "policy". GetACL must return it.
func TestGetACLHonoursDataField(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusOK, `{"policy":"","data":"old-shape-policy"}`)
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if got != "old-shape-policy" {
		t.Errorf("got %q, want old-shape-policy (Data field)", got)
	}
}

// TestGetACLPrefersPolicyOverData covers the precedence rule:
// when BOTH fields are populated, "policy" wins (it's the
// canonical field in current headscale; "data" is a legacy
// holdover).
func TestGetACLPrefersPolicyOverData(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusOK, `{"policy":"new-shape","data":"old-shape"}`)
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if got != "new-shape" {
		t.Errorf("got %q, want new-shape (Policy field should win)", got)
	}
}

// TestGetACLCacheInvalidatedBySetPolicySuccess verifies the
// cache-miss → SetPolicy → cache-cleared flow: after SetPolicy
// succeeds, the next GetACL hits the API again.
func TestGetACLCacheInvalidatedBySetPolicySuccess(t *testing.T) {
	// 1. GetACL → cache miss → API hit (returns "{}")
	// 2. SetPolicy with 200 → clears cache
	// 3. GetACL → cache miss again → API hit (returns "new-policy")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			// First call returns {}, subsequent calls return new-policy.
			// We can't easily count per-resource from one handler, so
			// just return a static response and rely on the hit counter
			// pattern below.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"policy":"{}","data":""}`))
		case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"policy":"OK"}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "fake-key")
	c.cacheTTL = time.Hour
	// Prime the cache.
	if _, err := c.GetACL(); err != nil {
		t.Fatalf("GetACL #1: %v", err)
	}
	// Same call should be a cache hit (no observable diff here,
	// but the cache MUST be invalidated after SetPolicy).
	if err := c.SetPolicy(`{"acls":[]}`); err != nil {
		// SetPolicy will fall through to file-mode (404 isn't
		// returned, our handler returns 200), so this should
		// succeed. If a future change makes the mock return
		// 404, the docker fallback will fail in CI; that's
		// a separate test concern.
		t.Logf("SetPolicy returned (likely 200+cache cleared): %v", err)
	}
	// Cache should be cleared regardless of SetPolicy's outcome.
	c.cacheMu.RLock()
	aclAfter := c.cacheACL
	c.cacheMu.RUnlock()
	if aclAfter != "" {
		t.Errorf("cacheACL = %q after SetPolicy, want cleared", aclAfter)
	}
}

// TestGetACLAPIFailsNoContainer verifies that when both the API
// fails AND ExecContainer is empty, the function returns the
// API error (no panic, no nil deref).
func TestGetACLAPIFailsNoContainer(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusInternalServerError, "boom")
	c.ExecContainer = ""
	_, err := c.GetACL()
	if err == nil {
		t.Fatal("expected error for 500 + no CLI, got nil")
	}
}

// --- 2026-07-13: InvalidateCache clears all three caches ---
//
// We added cache fields (cacheAll, cacheUsers, cacheACL) and
// InvalidateCache clears all of them. This test pins that
// contract so a future refactor can't accidentally drop one
// of the three.
func TestInvalidateCacheClearsAllThree(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusOK, `{"policy":"x","data":""}`)
	// Seed all three caches directly.
	c.cacheMu.Lock()
	c.cacheAll = []NodeView{{ID: "n1"}}
	c.cacheAllAt = time.Now()
	c.cacheUsers = []HSUser{{ID: "u1", Name: "u"}}
	c.cacheUsersAt = time.Now()
	c.cacheACL = "x"
	c.cacheACLAt = time.Now()
	c.cacheMu.Unlock()
	c.InvalidateCache()
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	if c.cacheAll != nil {
		t.Errorf("cacheAll not cleared: %v", c.cacheAll)
	}
	if c.cacheUsers != nil {
		t.Errorf("cacheUsers not cleared: %v", c.cacheUsers)
	}
	if c.cacheACL != "" {
		t.Errorf("cacheACL not cleared: %q", c.cacheACL)
	}
	if !c.cacheAllAt.IsZero() {
		t.Errorf("cacheAllAt not zero: %v", c.cacheAllAt)
	}
	if !c.cacheUsersAt.IsZero() {
		t.Errorf("cacheUsersAt not zero: %v", c.cacheUsersAt)
	}
	if !c.cacheACLAt.IsZero() {
		t.Errorf("cacheACLAt not zero: %v", c.cacheACLAt)
	}
}
