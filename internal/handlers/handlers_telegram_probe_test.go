package handlers

// 2026-07-14: Этап 14 v2 — Tests for the Tailscale reachability probe.
//
// We test the probe's behavior in three states:
//   - direct OK (Telegram responds, tailscaled NOT running)
//   - relay OK (Telegram responds, tailscaled running)
//   - unreachable (Telegram times out / 5xx / DNS fail)
//
// The HTTP target is mocked via httptest so the test doesn't
// require a real internet connection. Tailscale's "is it
// running" check is exercised by overriding the
// tailscaleSocketPaths package var and pointing it at a
// t.TempDir()-created fake socket file. This makes the test
// run the same production code path (probeTelegramAPIWithBase →
// isTailscaleActive) end-to-end without depending on the host's
// actual tailscaled state.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withFakeTailscaleSocket overrides the package-level
// tailscaleSocketPaths to point at a single fake socket file.
// The file is created or omitted based on `present`. A
// t.Cleanup restores the original slice so the next test
// isn't affected.
func withFakeTailscaleSocket(t *testing.T, present bool) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "tailscaled.sock")
	if present {
		if err := os.WriteFile(sockPath, []byte(""), 0o600); err != nil {
			t.Fatalf("write fake sock: %v", err)
		}
	}
	orig := tailscaleSocketPaths
	tailscaleSocketPaths = []string{sockPath}
	t.Cleanup(func() {
		tailscaleSocketPaths = orig
	})
}

// TestProbeDirectOK: Telegram responds 200, no tailscaled.
// Expected state: ProbeOKDirect.
func TestProbeDirectOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"x","username":"x"}}`)
	}))
	defer srv.Close()
	withFakeTailscaleSocket(t, false)

	got := probeTelegramAPIWithBase(context.Background(), "test-token", srv.URL)
	if got.State != ProbeOKDirect {
		t.Errorf("state: got %v, want ProbeOKDirect (msg=%q)", got.State, got.Message)
	}
	if got.Message == "" {
		t.Errorf("message should be non-empty on success")
	}
	if got.Latency < 0 {
		t.Errorf("latency: got %v, want >=0", got.Latency)
	}
}

// TestProbeRelayOK: Telegram responds 200, tailscaled IS running.
// Expected state: ProbeOKRelay.
func TestProbeRelayOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	withFakeTailscaleSocket(t, true)

	got := probeTelegramAPIWithBase(context.Background(), "test-token", srv.URL)
	if got.State != ProbeOKRelay {
		t.Errorf("state: got %v, want ProbeOKRelay (msg=%q)", got.State, got.Message)
	}
	if !strings.Contains(got.Message, "Tailscale") {
		t.Errorf("message should mention Tailscale, got %q", got.Message)
	}
}

// TestProbeUnreachable5xx: Telegram returns 503.
// Expected state: ProbeUnreachable.
func TestProbeUnreachable5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	withFakeTailscaleSocket(t, false)

	got := probeTelegramAPIWithBase(context.Background(), "test-token", srv.URL)
	if got.State != ProbeUnreachable {
		t.Errorf("state: got %v, want ProbeUnreachable (msg=%q)", got.State, got.Message)
	}
	if !strings.Contains(strings.ToLower(got.Message), "5xx") {
		t.Errorf("message should mention 5xx, got %q", got.Message)
	}
}

// TestProbeUnreachableConnRefused: HTTP target is not listening.
// Expected state: ProbeUnreachable.
func TestProbeUnreachableConnRefused(t *testing.T) {
	got := probeTelegramAPIWithBase(context.Background(), "test-token", "http://127.0.0.1:1/")
	if got.State != ProbeUnreachable {
		t.Errorf("state: got %v, want ProbeUnreachable (msg=%q)", got.State, got.Message)
	}
}

// TestProbeEmptyToken: no token configured.
// Expected: ProbeUnreachable with a clear "not configured" message.
func TestProbeEmptyToken(t *testing.T) {
	withFakeTailscaleSocket(t, false)
	got := probeTelegramAPIWithBase(context.Background(), "", "https://api.telegram.org")
	if got.State != ProbeUnreachable {
		t.Errorf("state: got %v, want ProbeUnreachable (msg=%q)", got.State, got.Message)
	}
	if !strings.Contains(got.Message, "not configured") {
		t.Errorf("message should explain the empty-token case, got %q", got.Message)
	}
}

// TestProbeUnreachable4xxIsOK: 401/404 are "reachable" — Telegram's
// edge responded, we just have a bad/rotated token. This is
// different from a 5xx (Telegram's edge is down) and from a
// connection refused (Telegram's edge is unreachable).
func TestProbeUnreachable4xxIsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"unauthorized"}`)
	}))
	defer srv.Close()
	withFakeTailscaleSocket(t, false)

	got := probeTelegramAPIWithBase(context.Background(), "rotated-token", srv.URL)
	if got.State != ProbeOKDirect {
		t.Errorf("state: got %v, want ProbeOKDirect (4xx = reachable; msg=%q)", got.State, got.Message)
	}
}

// TestProbeContextCancellation: client context expires before
// the request completes. Expected: ProbeUnreachable.
func TestProbeContextCancellation(t *testing.T) {
	// Server hangs forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	withFakeTailscaleSocket(t, false)

	// Use a context with a 100ms timeout — way shorter than
	// the test runner's patience, so the test fails fast.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got := probeTelegramAPIWithBase(ctx, "test-token", srv.URL)
	if got.State != ProbeUnreachable {
		t.Errorf("state: got %v, want ProbeUnreachable (msg=%q)", got.State, got.Message)
	}
}

// TestProbeStateString: stable string representations. The
// template uses these as CSS class suffixes, so they are
// part of the wire format.
func TestProbeStateString(t *testing.T) {
	cases := []struct {
		state TelegramProbeState
		want  string
	}{
		{ProbeUnreachable, "unreachable"},
		{ProbeOKDirect, "ok_direct"},
		{ProbeOKRelay, "ok_relay"},
		// Unknown / zero value should still render as
		// "unreachable" so an uninitialised struct in
		// the template doesn't produce an empty class.
		{TelegramProbeState(99), "unreachable"},
	}
	for _, c := range cases {
		if got := c.state.String(); got != c.want {
			t.Errorf("state %d: got %q, want %q", c.state, got, c.want)
		}
	}
}

// TestIsTailscaleActive: the socket-detection heuristic itself.
func TestIsTailscaleActive(t *testing.T) {
	// No socket — inactive.
	withFakeTailscaleSocket(t, false)
	if isTailscaleActive() {
		t.Errorf("expected inactive when no socket file present")
	}
	// Socket present — active.
	withFakeTailscaleSocket(t, true)
	if !isTailscaleActive() {
		t.Errorf("expected active when socket file present")
	}
}
