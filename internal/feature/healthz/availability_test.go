package healthz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewChecker_IntervalClamping pins the interval bounds.
// Sub-5s intervals are clamped UP to 5s; over-5min intervals
// are clamped DOWN to 5min. Out-of-bounds values would cause
// either a probe storm (1ms interval = 1000 probes/sec) or
// a stale UI (1h interval = operator never sees fresh status).
func TestNewChecker_IntervalClamping(t *testing.T) {
	cases := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{"1ms clamped to 5s", 1 * time.Millisecond, 5 * time.Second},
		{"0 clamped to 5s", 0, 5 * time.Second},
		{"5s kept as-is", 5 * time.Second, 5 * time.Second},
		{"30s default", 30 * time.Second, 30 * time.Second},
		{"5min kept as-is", 5 * time.Minute, 5 * time.Minute},
		{"1h clamped to 5min", 1 * time.Hour, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewChecker("http://h:1", "http://p:1", nil, tc.input)
			if c.Interval != tc.expected {
				t.Errorf("got %v, want %v", c.Interval, tc.expected)
			}
		})
	}
}

// TestChecker_HeadscaleOK spins up a tiny HTTP server that
// mimics headscale's /health endpoint, points the checker at
// it, runs one probe, and verifies the cached status is OK
// with the right URL and detail.
func TestChecker_HeadscaleOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"pass"}`))
	}))
	defer srv.Close()

	c := NewChecker(srv.URL, "", nil, 30*time.Second)
	c.runOnceInTest(t) // synchronous, no goroutine
	st := mustFind(t, c.Snapshot(), IntegrationHeadscale)
	if !st.OK {
		t.Errorf("expected headscale OK; got error: %q", st.Error)
	}
	if st.Detail == "" {
		t.Errorf("expected non-empty detail (response body); got empty")
	}
	if st.URL != srv.URL {
		t.Errorf("URL mismatch: got %q, want %q", st.URL, srv.URL)
	}
	if st.LastChecked.IsZero() {
		t.Errorf("LastChecked should be set after a successful probe")
	}
	// LatencyMS is `time.Since(t0).Milliseconds()` — can be
	// 0 on fast localhost (e.g. the test's httptest.NewServer
	// returns in <1ms on modern hardware). We only assert
	// "non-negative" (i.e. the timer is wired correctly);
	// asserting "positive" was a flaky-test bug. 2026-08-11
	// v1.0.0.1 fix.
	if st.LatencyMS < 0 {
		t.Errorf("latency should be non-negative, got %d ms", st.LatencyMS)
	}
}

// TestChecker_HeadscaleDown points the checker at a port no
// one listens on. Verifies the cached status is NOT OK and
// the error field is populated (operator sees the failure
// reason on /admin/services). Also pins the contract that
// a failed probe still records LastChecked — otherwise the
// AllOK() helper would treat the failure as "not configured"
// (because IsZero=true) and the operator would never see the
// failure on the /readyz response.
func TestChecker_HeadscaleDown(t *testing.T) {
	c := NewChecker("http://127.0.0.1:1/should-fail", "", nil, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationHeadscale)
	if st.OK {
		t.Errorf("expected headscale DOWN; got OK")
	}
	if st.Error == "" {
		t.Errorf("expected non-empty error on failed probe; got empty")
	}
	if st.LastChecked.IsZero() {
		t.Errorf("LastChecked should be set on failed probe (AllOK relies on it)")
	}
}

// TestChecker_EmptyURLSkipped — when HEADSCALE_URL is empty
// (operator without headscale, e.g. read-only deploys) the
// checker must NOT report an error; it must report "not
// configured" (IsZero=true) so /readyz treats it as skipped
// rather than failed.
func TestChecker_EmptyURLSkipped(t *testing.T) {
	c := NewChecker("", "http://h:1", nil, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationHeadscale)
	if !st.IsZero() {
		t.Errorf("expected IsZero=true when URL is empty; got LastChecked=%v OK=%v", st.LastChecked, st.OK)
	}
	if st.OK {
		t.Errorf("expected OK=false when URL is empty; got true")
	}
}

// TestChecker_HeadplaneOK spins up a second server, points
// the headplane check at it, verifies the cached status.
//
// The v1.3.5 fix probes HEADPLANE_URL + "/admin/healthz"
// (headplane 0.6.x is distroless — the only public health
// endpoint is /admin/healthz, NOT the root which returns 404).
// The fake server below only responds 200 on /admin/healthz
// and 404 on everything else, proving the new probe path
// is correct and the old root-probe would fail.
func TestChecker_HeadplaneOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/healthz" {
			w.WriteHeader(200)
			w.Write([]byte(`{"status":"OK"}`))
			return
		}
		// Mimic the real distroless headplane: 404 on root.
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := NewChecker("", srv.URL, nil, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationHeadplane)
	if !st.OK {
		t.Errorf("expected headplane OK; got error: %q", st.Error)
	}
}

// TestChecker_HeadplaneAlreadyIncludesPath verifies the
// no-double-append guard. If the operator sets
// HEADPLANE_URL=http://headplane:50445/admin/healthz, the
// checker should probe that URL verbatim (not append
// /admin/healthz again).
func TestChecker_HeadplaneAlreadyIncludesPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"OK"}`))
	}))
	defer srv.Close()
	c := NewChecker("", srv.URL+"/admin/healthz", nil, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationHeadplane)
	if !st.OK {
		t.Errorf("expected headplane OK; got error: %q", st.Error)
	}
	if gotPath != "/admin/healthz" {
		t.Errorf("expected probe path /admin/healthz (no double-append); got %q", gotPath)
	}
}

// TestChecker_TailscaleFn — verifies the local-state probe
// (no HTTP). TailscaleFn returns online + detail; the checker
// must surface both.
func TestChecker_TailscaleFn(t *testing.T) {
	c := NewChecker("", "", func() (bool, string) {
		return true, "online (100.64.0.18)"
	}, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationTailscale)
	if !st.OK {
		t.Errorf("expected tailscale OK; got error: %q", st.Error)
	}
	if !strings.Contains(st.Detail, "100.64.0.18") {
		t.Errorf("expected detail to include IP; got %q", st.Detail)
	}
}

// TestChecker_TailscaleFnNil — when TailscaleFn is nil
// (Tailscale disabled in compose) the integration is
// reported as "not configured" without running anything.
func TestChecker_TailscaleFnNil(t *testing.T) {
	c := NewChecker("", "", nil, 30*time.Second)
	c.runOnceInTest(t)
	st := mustFind(t, c.Snapshot(), IntegrationTailscale)
	if !st.IsZero() {
		t.Errorf("expected IsZero=true when TailscaleFn is nil; got OK=%v LastChecked=%v", st.OK, st.LastChecked)
	}
}

// TestAvailability_AllOK — verifies the AllOK helper. With
// all probes OK, returns true. With any probe failing, false.
// "Never checked" integrations are treated as "skipped" and
// don't fail AllOK.
func TestAvailability_AllOK(t *testing.T) {
	c := NewChecker("http://127.0.0.1:1", "http://127.0.0.1:1", nil, 30*time.Second)
	c.runOnceInTest(t)
	a := c.Snapshot()
	if a.AllOK() {
		t.Errorf("AllOK() should be false when 2 probes fail")
	}
	// Inject an "OK" record for one to verify the AND-of-all
	// semantics.
	a.Integrations[0].OK = true
	a.Integrations[0].LastChecked = time.Now()
	if a.AllOK() {
		t.Errorf("AllOK() should be false when one probe still fails")
	}
	a.Integrations[1].OK = true
	a.Integrations[1].LastChecked = time.Now()
	if !a.AllOK() {
		t.Errorf("AllOK() should be true when all probes pass")
	}
	// "Never checked" should not fail AllOK.
	a.Integrations[2].IsZero()
	a.Integrations[2].LastChecked = time.Time{}
	if !a.AllOK() {
		t.Errorf("AllOK() should ignore never-checked integrations")
	}
}

// TestAvailability_JSON — verifies the Availability struct
// marshals to a stable JSON shape (operator-facing API
// contract; tools that consume the JSON shouldn't break
// when we add fields).
func TestAvailability_JSON(t *testing.T) {
	a := Availability{
		GeneratedAt: time.Now().UTC(),
		Integrations: []IntegrationStatus{
			{
				ID:          IntegrationHeadscale,
				Label:       "headscale",
				URL:         "http://x:1",
				OK:          true,
				LastChecked: time.Now().UTC(),
				LatencyMS:   42,
				Detail:      `{"status":"pass"}`,
			},
		},
	}
	b, err := json.Marshal(&a)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"id":"headscale"`,
		`"label":"headscale"`,
		`"ok":true`,
		`"latency_ms":42`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q; got %s", want, s)
		}
	}
}

// runOnceInTest is a test-only wrapper that runs runOnce
// synchronously (Start() also runs runOnce + spawns a
// goroutine; the goroutine complicates the test timing).
func (c *Checker) runOnceInTest(t *testing.T) {
	t.Helper()
	c.runOnce(context.TODO())
}

// mustFind returns the integration status with the given
// id, failing the test if not present.
func mustFind(t *testing.T, a *Availability, id IntegrationKind) IntegrationStatus {
	t.Helper()
	st, ok := a.Find(id)
	if !ok {
		t.Fatalf("integration %q not in snapshot: %+v", id, a.Integrations)
	}
	return st
}
