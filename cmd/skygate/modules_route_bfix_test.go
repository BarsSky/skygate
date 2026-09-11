// B-fix-modules-route (2026-09-11) — regression test for the
// /admin/modules/{name}/{action...} route pattern.
//
// Live-caught bug: pre-fix, B-mod-admin registered the sub-feature
// POST route as /admin/modules/{name}/{action}. Go 1.22 mux's {action}
// wildcard matches a single path segment, so /admin/modules/tailscale/
// sub/cluster (2 segments after {name}) didn't match — the request
// fell through to the `/` catch-all handler which 302'd to /dashboard.
// Operator-visible symptom: the sub-feature toggle forms on
// /admin/modules/tailscale submitted successfully but the page
// bounced to /dashboard with no state change, no audit row, and
// state.json unchanged.
//
// The fix: replace {action} with {action...} to capture the
// multi-segment "sub/<name>" action path. This test pins the new
// route so a future refactor doesn't regress it.

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestModulesSubRouteMatchesMultiSegmentAction verifies that
// /admin/modules/{name}/{action...} matches /admin/modules/tailscale/
// sub/cluster (multi-segment {action}) AND that PathValue("action")
// returns the full "sub/cluster" string (NOT just "sub").
//
// Run with: go test ./cmd/skygate -run TestModulesSubRouteMatchesMultiSegmentAction
func TestModulesSubRouteMatchesMultiSegmentAction(t *testing.T) {
	var gotName, gotAction string
	var matched bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/modules/{name}/{action...}", func(w http.ResponseWriter, r *http.Request) {
		gotName = r.PathValue("name")
		gotAction = r.PathValue("action")
		matched = true
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name, url, wantName, wantAction string
	}{
		{
			name:       "single-segment action (install/start/stop/enable/disable)",
			url:        "/admin/modules/tailscale/install",
			wantName:   "tailscale",
			wantAction: "install",
		},
		{
			name:       "multi-segment action (sub/cluster)",
			url:        "/admin/modules/tailscale/sub/cluster",
			wantName:   "tailscale",
			wantAction: "sub/cluster",
		},
		{
			name:       "multi-segment action (sub/telegram)",
			url:        "/admin/modules/tailscale/sub/telegram",
			wantName:   "tailscale",
			wantAction: "sub/telegram",
		},
		{
			name:       "multi-segment action (sub/derp)",
			url:        "/admin/modules/tailscale/sub/derp",
			wantName:   "tailscale",
			wantAction: "sub/derp",
		},
		{
			name:       "multi-segment action (sub/exit)",
			url:        "/admin/modules/tailscale/sub/exit",
			wantName:   "tailscale",
			wantAction: "sub/exit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched = false
			gotName = ""
			gotAction = ""
			req := httptest.NewRequest("POST", tc.url, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if !matched {
				t.Fatalf("URL %q did not match /admin/modules/{name}/{action...}", tc.url)
			}
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if gotAction != tc.wantAction {
				t.Errorf("action = %q, want %q (full multi-segment capture)", gotAction, tc.wantAction)
			}
		})
	}
}

// TestModulesSubRouteDoesNotMatchUnrelatedPaths ensures the
// /admin/modules/{name}/{action...} pattern doesn't over-match.
// /admin/modules (no {name}) should fall through to the GET list
// handler, not this POST handler.
func TestModulesSubRouteDoesNotMatchUnrelatedPaths(t *testing.T) {
	mux := http.NewServeMux()
	postMatched := false
	mux.HandleFunc("POST /admin/modules/{name}/{action...}", func(w http.ResponseWriter, r *http.Request) {
		postMatched = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /admin/modules", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// /admin/modules (no {name}, no {action}) — must NOT match POST handler
	req := httptest.NewRequest("POST", "/admin/modules", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if postMatched {
		t.Errorf("POST /admin/modules matched POST handler — should fall through")
	}
}
