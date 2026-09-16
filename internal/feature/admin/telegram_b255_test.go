// v1.5.8 (B255): unit tests for the Telegram background
// polling helpers — renderProbeHTML, renderContainerHTML,
// and tailscalePeerLatencies. These are pure helpers that
// don't touch the DB, so they're exercised in isolation
// without the OpenTestPG harness.
//
// What these tests cover (the B255 contract):
//   - renderProbeHTML produces the structural HTML the JS
//     expects (id="telegram-probe-slot", correct alert class
//     per state, troubleshooting list rendered for the
//     unreachable case, conditional container tip).
//   - renderContainerHTML produces the structural HTML the JS
//     expects (id="telegram-container-slot", Available=false
//     branch with stderr pre, Available=true branch with the
//     KV table, HasAcceptIssue branch with the reapply form).
//   - tailscalePeerLatencies parses both the legacy
//     `PeerLatency: 12.3` and the modern
//     `PeerLatency: {"ms": 12.3}` shapes, and returns
//     (empty, nil) when tailscaled isn't running.
//
// What this file does NOT cover (out of scope for unit tests):
//   - The full handler chain (AdminTelegramProbeBg +
//     AdminTelegramContainerBg) — that's exercised by the
//     live admin UI on the prod VM (192.168.13.69).
//   - The set_nearest_egress handler — that requires SSH
//     + headscale integration; the live "Pin nearest" button
//     on /admin/telegram is the integration test.

package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"skygate/internal/i18n"
)

// TestMain installs the i18n global catalog so the package-level
// i18n.T() / i18n.Tf() helpers used by renderProbeHTML +
// renderContainerHTML + handleTelegramSetNearestEgress can find
// translations. Without this, every test sees raw keys
// ("telegram.probe_ok_direct_label") instead of the translated
// text — the helper functions correctly fall back to the key
// when GlobalCatalog is nil, which is the documented fail-safe
// behaviour for production code paths that aren't on the
// funcmap render path.
//
// Why TestMain and not a per-test init: the catalog is a
// shared package-level singleton (GlobalCatalog) and only
// needs to be installed once for the whole binary.
func TestMain(m *testing.M) {
	i18n.SetGlobal(i18n.New())
	m.Run()
}

// newTestCatalog returns a fresh i18n catalog for tests so we
// don't depend on the package-level state in i18n_test.go.
// Unused in most tests (renderProbeHTML takes a lang string
// directly) but kept for future tests that need the catalog.
func newTestCatalog() *i18n.Catalog { return i18n.New() }

// TestRenderProbeHTML_OkDirect pins the structural shape the
// JS depends on for the ok_direct case.
func TestRenderProbeHTML_OkDirect(t *testing.T) {
	probe := TelegramProbeResult{
		State:     ProbeOKDirect,
		Message:   "Reachable via direct internet",
		Latency:   123 * time.Millisecond,
		LatencyMS: "123ms",
	}
	out := renderProbeHTML(probe, ContainerTailscaleState{}, "ru")
	for _, want := range []string{
		`id="telegram-probe-slot"`,
		`class="alert alert-probe probe-ok_direct"`,
		`<i class="fa-solid fa-globe"></i>`,
		`Telegram API: доступен (прямой интернет)`,
		`123ms`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderProbeHTML(ok_direct) missing %q\n-- got --\n%s", want, out)
		}
	}
	// Make sure troubleshooting ISN'T rendered for the
	// reachable case.
	if strings.Contains(out, "Что проверить:") {
		t.Errorf("renderProbeHTML(ok_direct) should not render the troubleshooting block")
	}
}

// TestRenderProbeHTML_Unreachable_ContainerOff pins the
// B-bug-fix from 2026-09-15: when the container is
// unreachable (Available=false), the FIRST troubleshooting
// tip is the "tailscaled not running in container" hint,
// not the generic "advertise-routes on relay" hint.
func TestRenderProbeHTML_Unreachable_ContainerOff(t *testing.T) {
	probe := TelegramProbeResult{
		State:     ProbeUnreachable,
		Message:   "api.telegram.org timeout",
		LatencyMS: "5000ms",
	}
	container := ContainerTailscaleState{
		Available: false,
		RawStderr: "docker exec failed: timeout",
	}
	out := renderProbeHTML(probe, container, "ru")
	for _, want := range []string{
		`class="alert alert-probe probe-unreachable"`,
		`<i class="fa-solid fa-triangle-exclamation"></i>`,
		`Telegram API: недоступен`,
		`Что проверить:`,
		// B-bug-fix: container_off tip fires first.
		`tailscaled не запущен`,
		// Then the 4 generic tips.
		`advertise-routes`,
		`headscale nodes list`,
		`make tailscale-update-telegram-routes`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderProbeHTML(unreachable, container_off) missing %q\n-- got --\n%s", want, out)
		}
	}
}

// TestRenderProbeHTML_Unreachable_RouteAllOff pins the
// conditional tip that fires when RouteAll=false on the
// container — the operator's exact live cause on 2026-08-25.
func TestRenderProbeHTML_Unreachable_RouteAllOff(t *testing.T) {
	probe := TelegramProbeResult{State: ProbeUnreachable}
	container := ContainerTailscaleState{
		Available:      true,
		Hostname:       "skygate-skygate-1",
		RouteAll:       false,
		HasAcceptIssue: true,
	}
	out := renderProbeHTML(probe, container, "ru")
	if !strings.Contains(out, `tailscale set --accept-routes=false`) {
		t.Errorf("renderProbeHTML(unreachable, RouteAll=false) should mention accept-routes=false\n-- got --\n%s", out)
	}
	// Should NOT also fire the container_off tip (mutually
	// exclusive branches in the template).
	if strings.Contains(out, "tailscaled не запущен") {
		t.Errorf("renderProbeHTML(unreachable, RouteAll=false) should NOT mention tailscaled-not-running (the container IS available)\n-- got --\n%s", out)
	}
}

// TestRenderProbeHTML_OkRelay pins the ok_relay case.
func TestRenderProbeHTML_OkRelay(t *testing.T) {
	probe := TelegramProbeResult{
		State:       ProbeOKRelay,
		Message:     "Reachable via Tailscale relay",
		LatencyMS:   "45ms",
		ResolvedIPs: []string{"91.108.4.1", "2001:67c:4e8::1"},
	}
	out := renderProbeHTML(probe, ContainerTailscaleState{}, "ru")
	for _, want := range []string{
		`class="alert alert-probe probe-ok_relay"`,
		`<i class="fa-solid fa-route"></i>`,
		`Telegram API: доступен (через Tailscale relay)`,
		`<code>91.108.4.1</code>`,
		`<code>2001:67c:4e8::1</code>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderProbeHTML(ok_relay) missing %q\n-- got --\n%s", want, out)
		}
	}
}

// TestRenderProbeHTML_EscapesLatency pins that dynamic strings
// (LatencyMS, Message) are HTML-escaped. The probe result is
// built from a remote HTTP response, so this guards against
// XSS in the bg-poll response (the pre-B255 sync render went
// through Go html/template which auto-escapes; the B255 path
// uses string-builder + html.EscapeString — both must produce
// the same safe output).
func TestRenderProbeHTML_EscapesLatency(t *testing.T) {
	probe := TelegramProbeResult{
		State:     ProbeUnreachable,
		Message:   "<script>alert(1)</script>",
		LatencyMS: "100ms",
	}
	out := renderProbeHTML(probe, ContainerTailscaleState{}, "ru")
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("renderProbeHTML must escape script tags in Message\n-- got --\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("renderProbeHTML should HTML-escape &lt; and &gt;\n-- got --\n%s", out)
	}
}

// TestRenderContainerHTML_Unavailable pins the
// `Available=false` branch — the alert + stderr pre block.
func TestRenderContainerHTML_Unavailable(t *testing.T) {
	ct := ContainerTailscaleState{
		Available: false,
		RawStderr: "docker exec failed: timeout",
	}
	out := renderContainerHTML(ct, "csrf-test", "ru")
	for _, want := range []string{
		`id="telegram-container-slot"`,
		`<div class="alert alert-warn">`,
		`Не удалось прочитать состояние tailscale`,
		`docker exec failed: timeout`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderContainerHTML(unavailable) missing %q\n-- got --\n%s", want, out)
		}
	}
	// Should NOT render the table when unavailable.
	if strings.Contains(out, `<table class="kv"`) {
		t.Errorf("renderContainerHTML(unavailable) should not render the KV table")
	}
}

// TestRenderContainerHTML_AvailableRouteAllOff pins the
// "available but RouteAll=false" case — the alert + the
// "Re-apply accept-routes" form with the CSRF field.
func TestRenderContainerHTML_AvailableRouteAllOff(t *testing.T) {
	ct := ContainerTailscaleState{
		Available:     true,
		Hostname:      "skygate-skygate-1",
		BackendState:  "Running",
		IP4:           "100.64.0.20",
		IP6:           "",
		RouteAll:      false,
		AdvertiseTags: []string{"tag:skygate"},
		ExitNodeID:    "",
		HasAcceptIssue: true,
	}
	out := renderContainerHTML(ct, "csrf-token-XYZ", "ru")
	for _, want := range []string{
		`id="telegram-container-slot"`,
		`<table class="kv"`,
		`<code>skygate-skygate-1</code>`,
		`<code>Running</code>`,
		`<code>100.64.0.20</code>`,
		`<span class="badge badge-danger">OFF</span>`,
		`<code>tag:skygate</code>`,
		// Form with CSRF.
		`<form action="/admin/telegram" method="POST"`,
		`<input type="hidden" name="csrf" value="csrf-token-XYZ">`,
		`<input type="hidden" name="action" value="reapply_accept_routes">`,
		`Re-apply accept-routes`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderContainerHTML(RouteAll=false) missing %q\n-- got --\n%s", want, out)
		}
	}
}

// TestRenderContainerHTML_AvailableRouteAllOn pins the
// healthy case — no reapply form, route-all=on badge, no
// HasAcceptIssue alert.
func TestRenderContainerHTML_AvailableRouteAllOn(t *testing.T) {
	ct := ContainerTailscaleState{
		Available:     true,
		Hostname:      "skygate-skygate-1",
		BackendState:  "Running",
		IP4:           "100.64.0.20",
		RouteAll:      true,
		AdvertiseTags: []string{"tag:skygate"},
		ExitNodeID:    "1234",
		HasAcceptIssue: false,
	}
	out := renderContainerHTML(ct, "csrf-token", "ru")
	for _, want := range []string{
		`<span class="badge badge-success">ON</span>`,
		`<code>1234</code>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderContainerHTML(RouteAll=true) missing %q\n-- got --\n%s", want, out)
		}
	}
	// Should NOT render the reapply form when HasAcceptIssue=false.
	if strings.Contains(out, "Re-apply accept-routes") {
		t.Errorf("renderContainerHTML(RouteAll=true) should NOT render the reapply form")
	}
}

// TestRenderContainerHTML_EscapesHostname pins that dynamic
// strings (Hostname, BackendState, etc.) are HTML-escaped.
func TestRenderContainerHTML_EscapesHostname(t *testing.T) {
	ct := ContainerTailscaleState{
		Available:    true,
		Hostname:     "<script>alert(1)</script>",
		BackendState: "Running",
		RouteAll:     true,
	}
	out := renderContainerHTML(ct, "csrf", "ru")
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("renderContainerHTML must escape Hostname")
	}
}

// TestTailscalePeerLatencies_ParseObjectShape pins the
// modern `PeerLatency: {"ms": N}` shape — what tailscale
// 1.40+ emits.
func TestTailscalePeerLatencies_ParseObjectShape(t *testing.T) {
	// Save the real exec + tailscaled check; restore after
	// the test. The function short-circuits on
	// tailscaledRunning()=false (which is the test env
	// default — there's no tailscaled.sock on the host
	// that runs `go test`), so we MUST override both.
	oldRun := tailscaledRunningFn
	oldExec := tailscaleStatusExecFn
	tailscaledRunningFn = func() bool { return true }
	tailscaleStatusExecFn = func(ctx context.Context) ([]byte, error) {
		return []byte(`{
  "BackendState": "Running",
  "Peer": {
    "nodekey:1": { "HostName": "emilia",    "PeerLatency": {"ms": 12.3} },
    "nodekey:2": { "HostName": "sharlotta", "PeerLatency": {"ms": 45.6} },
    "nodekey:3": { "HostName": "no-latency","PeerLatency": {} }
  }
}`), nil
	}
	defer func() {
		tailscaledRunningFn = oldRun
		tailscaleStatusExecFn = oldExec
	}()

	got, err := tailscalePeerLatencies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(got), got)
	}
	if got["emilia"] != 12.3 {
		t.Errorf("emilia latency = %v, want 12.3", got["emilia"])
	}
	if got["sharlotta"] != 45.6 {
		t.Errorf("sharlotta latency = %v, want 45.6", got["sharlotta"])
	}
	if _, ok := got["no-latency"]; ok {
		t.Errorf("no-latency should be skipped (empty PeerLatency)")
	}
}

// TestTailscalePeerLatencies_ParseLegacyBareNumber pins the
// legacy `PeerLatency: 12.3` (bare number) shape — what
// older tailscale clients emit. The handler must accept
// both, otherwise the latency map is empty on upgrade.
func TestTailscalePeerLatencies_ParseLegacyBareNumber(t *testing.T) {
	oldRun := tailscaledRunningFn
	oldExec := tailscaleStatusExecFn
	tailscaledRunningFn = func() bool { return true }
	tailscaleStatusExecFn = func(ctx context.Context) ([]byte, error) {
		return []byte(`{
  "BackendState": "Running",
  "Peer": {
    "nodekey:1": { "HostName": "emilia",    "PeerLatency": 12.3 },
    "nodekey:2": { "HostName": "sharlotta", "PeerLatency": 45 }
  }
}`), nil
	}
	defer func() {
		tailscaledRunningFn = oldRun
		tailscaleStatusExecFn = oldExec
	}()

	got, err := tailscalePeerLatencies()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["emilia"] != 12.3 {
		t.Errorf("emilia latency = %v, want 12.3 (legacy bare number)", got["emilia"])
	}
	if got["sharlotta"] != 45 {
		t.Errorf("sharlotta latency = %v, want 45 (legacy bare int)", got["sharlotta"])
	}
}

// TestTailscalePeerLatencies_TailscaledDown pins the
// "tailscaled not running" path — the handler must return
// (empty, nil) rather than an error so the caller can fall
// back to the first enabled relay instead of erroring out.
//
// Note: the exec error path is exercised via the
// tailscaledRunning check at the top of the function — we
// don't override the exec fn here because the early return
// short-circuits before the exec runs. To exercise the
// exec-error path, override tailscaledRunning (see the
// "exec error" test below).
func TestTailscalePeerLatencies_TailscaledDown(t *testing.T) {
	oldRun := tailscaledRunningFn
	tailscaledRunningFn = func() bool { return false }
	defer func() { tailscaledRunningFn = oldRun }()

	got, err := tailscalePeerLatencies()
	if err != nil {
		t.Errorf("expected nil error when tailscaled is down (fallback path), got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map when tailscaled is down, got %v", got)
	}
}

// TestTailscalePeerLatencies_ExecError pins the "tailscaled
// is up but `tailscale status --json` fails" path — the
// caller (handleTelegramSetNearestEgress) audits the
// error and falls back to the first enabled relay.
func TestTailscalePeerLatencies_ExecError(t *testing.T) {
	oldRun := tailscaledRunningFn
	oldExec := tailscaleStatusExecFn
	tailscaledRunningFn = func() bool { return true }
	tailscaleStatusExecFn = func(ctx context.Context) ([]byte, error) {
		return nil, errFakeExec
	}
	defer func() {
		tailscaledRunningFn = oldRun
		tailscaleStatusExecFn = oldExec
	}()

	got, err := tailscalePeerLatencies()
	if err == nil {
		t.Errorf("expected non-nil error from exec failure, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on exec failure, got %v", got)
	}
}

// errFakeExec is a sentinel error for the "exec failure"
// tests; declared package-level so tests can reuse it.
var errFakeExec = &fakeErr{msg: "tailscale status: simulated exec failure"}

// fakeErr is a minimal error type for tests.
type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
