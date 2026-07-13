package handlers

// 2026-07-14: Этап 14 v2 — Tailscale reachability probe for the bot.
//
// The probe is what /admin/telegram uses to surface a 3-state banner
// on every page load:
//
//   ok_direct    — api.telegram.org reachable, no Tailscale running.
//                  Means the VPS is in a jurisdiction where Telegram
//                  isn't blocked (or the operator's egress works
//                  directly). The bot polling works through eth0.
//
//   ok_relay     — api.telegram.org reachable, Tailscale is running.
//                  Means the bot is going through a relay (a tailnet
//                  node that advertises the Telegram IP ranges as
//                  subnet routes). This is the RF-deployment mode.
//
//   unreachable  — api.telegram.org NOT reachable. The banner shows
//                  troubleshooting steps (check the relay, check the
//                  subnet routes, check the local firewall).
//
// We intentionally do NOT distinguish "ok_relay" from "ok_direct" by
// the route the request actually took (which would need raw socket
// introspection). The heuristic "is tailscaled running" is good enough:
// on a non-RF deployment tailscaled isn't started at all (entrypoint
// skips it when TS_AUTHKEY_FILE is unset), so the probe reports
// "ok_direct". On an RF deployment tailscaled is always running, so
// any successful probe is "ok_relay".
//
// The probe is cheap (single HEAD with a 5s timeout) and runs
// synchronously on every GET /admin/telegram. We don't cache the
// result — the admin page is hit infrequently and stale "OK" banners
// are worse than a 200ms page load.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// TelegramProbeState is the discrete outcome of a probe. The integer
// values are stored in the DB / rendered in the template, so they
// are part of the wire format — do NOT renumber.
type TelegramProbeState int

const (
	// ProbeUnreachable: api.telegram.org did not respond within the
	// timeout (5s). Causes: RF block + no relay, relay down, relay's
	// advertised routes don't cover the resolved IPs (Telegram added
	// a new range), local firewall, etc.
	ProbeUnreachable TelegramProbeState = iota

	// ProbeOKDirect: api.telegram.org responded, and tailscaled is
	// not running. The bot's polling goes through eth0 directly.
	// Typical for non-RF VPSes that don't need Tailscale for egress.
	ProbeOKDirect

	// ProbeOKRelay: api.telegram.org responded, and tailscaled is
	// running. The bot's polling goes through a tailnet node (the
	// relay). Typical for RF deployments.
	ProbeOKRelay
)

// String renders the state as a stable lower-case identifier used
// in the template (for the CSS class hook, e.g. .probe-ok-relay).
func (s TelegramProbeState) String() string {
	switch s {
	case ProbeOKDirect:
		return "ok_direct"
	case ProbeOKRelay:
		return "ok_relay"
	default:
		return "unreachable"
	}
}

// TelegramProbeResult is what the probe returns. The latency is
// recorded for the template (so the admin can see "OK relay,
// 230ms" vs "OK relay, 1.8s" — useful when the relay is slow
// but not dead). LatencyMS is the pre-formatted milliseconds
// string (e.g. "230ms", "1820ms") because Go templates can't
// easily divide a Duration by 1_000_000 inside the template
// (the .Nanoseconds / 1000000 syntax trips the parser on
// some template-engine builds, and even when it works, mixing
// integer division with method-call results is fragile).
type TelegramProbeResult struct {
	State   TelegramProbeState
	Message string
	Latency time.Duration
	// LatencyMS is the human-readable latency, "230ms" / "1820ms".
	// Empty when the probe didn't actually make a request
	// (e.g. the empty-token case). The template prefers
	// LatencyMS over Latency for display.
	LatencyMS string
	// ResolvedIPs is the set of A/AAAA records api.telegram.org
	// resolved to at probe time. The template uses this to show
	// "Telegram returned 91.108.56.130, 91.108.56.131" — useful
	// for the operator to verify those IPs are in the relay's
	// advertised subnet routes.
	ResolvedIPs []string
}

// formatLatencyMS converts a Duration to "<n>ms" with integer
// division. Negative or zero returns "" (the template treats
// that as "no measurement").
func formatLatencyMS(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	ms := d.Milliseconds()
	return fmt.Sprintf("%dms", ms)
}

// probeTelegramAPI is the public entry point used by the handler.
// It hard-codes the production Telegram API base. The testable
// variant is probeTelegramAPIWithBase below — the handler always
// calls this entry point, so production code can't accidentally
// be pointed at a test endpoint.
func probeTelegramAPI(ctx context.Context, token string) TelegramProbeResult {
	return probeTelegramAPIWithBase(ctx, token, "https://api.telegram.org")
}

// probeTelegramAPIWithBase does a real HEAD against the
// api.telegram.org getMe endpoint. It uses an explicit context
// with a 5-second timeout so a hanging connection can't stall
// the admin page.
//
// We pass the bot token to /getMe so the request is well-formed
// even when the token is rotated. The probe doesn't care about
// the response body, only that the HTTP round-trip succeeded.
func probeTelegramAPIWithBase(ctx context.Context, token, apiBase string) TelegramProbeResult {
	start := time.Now()
	if token == "" {
		// No token configured — the probe can't make a real
		// request. Return "unreachable" with an explanatory
		// message; the template's "configured" badge handles
		// the rest.
		return TelegramProbeResult{
			State:   ProbeUnreachable,
			Message: "Telegram bot token not configured — save one to enable the probe",
		}
	}
	endpoint := strings.TrimRight(apiBase, "/") + "/bot" + token + "/getMe"

	// We do a real GET (not HEAD) because some HTTP middleboxes
	// reject HEAD. 5s timeout, no retries — a slow probe is worse
	// than no probe (admin waits 5s on a broken bot).
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TelegramProbeResult{
			State:    ProbeUnreachable,
			Message:  "build request: " + err.Error(),
			Latency:  time.Since(start),
			LatencyMS: formatLatencyMS(time.Since(start)),
		}
	}
	client := &http.Client{
		// Tight transport-level timeout as a backstop. The
		// context timeout above fires first in practice.
		Timeout: 6 * time.Second,
	}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return TelegramProbeResult{
			State:    ProbeUnreachable,
			Message:  err.Error(),
			Latency:  latency,
			LatencyMS: formatLatencyMS(latency),
		}
	}
	defer resp.Body.Close()

	// Telegram's getMe returns 200 + JSON even for an invalid
	// token ({"ok":false,"error_code":401,...}). We treat any
	// 2xx/4xx as "reachable" (we got a real response from
	// Telegram's edge); 5xx is "unreachable" (Telegram's edge
	// is having a problem). 401/404 here is normal — the token
	// might be freshly rotated and the cached chat_id is stale.
	if resp.StatusCode >= 500 {
		return TelegramProbeResult{
			State:    ProbeUnreachable,
			Message:  "api.telegram.org 5xx: HTTP " + resp.Status,
			Latency:  latency,
			LatencyMS: formatLatencyMS(latency),
		}
	}

	// Resolve api.telegram.org for the diagnostic display.
	// This is a separate DNS lookup, not the one inside the HTTP
	// client; we do it explicitly so the operator can see which
	// IPs we hit and verify they're covered by the relay's
	// advertised subnet routes.
	ips := resolveTelegramAPI()

	state := ProbeOKDirect
	message := "Reachable via direct internet"
	if isTailscaleActive() {
		state = ProbeOKRelay
		message = "Reachable via Tailscale relay (subnet route)"
	}
	return TelegramProbeResult{
		State:      state,
		Message:    message,
		Latency:    latency,
		LatencyMS:  formatLatencyMS(latency),
		ResolvedIPs: ips,
	}
}

// tailscaleSocketPaths is the list of file paths where
// tailscaled's control socket is expected to exist. The
// production values are the standard Tailscale install
// locations; the test suite overrides this slice via
// t.Setenv + a package-level indirection (see
// handlers_telegram_probe_test.go) so the same
// isTailscaleActive() function is exercised end-to-end
// without depending on the host's actual tailscaled state.
//
// We use a package-level var (not a function-level
// constant) so tests can swap it via t.Cleanup without
// leaking state between subtests.
var tailscaleSocketPaths = []string{
	"/var/run/tailscale/tailscaled.sock",
	"/tmp/tailscaled.sock", // fallback for non-standard paths
}

// isTailscaleActive is the heuristic for "is tailscaled running
// in this container?". We check the control-socket file's
// existence; tailscaled creates /var/run/tailscale/tailscaled.sock
// on startup and removes it on shutdown.
//
// In a non-RF deployment the entrypoint never starts tailscaled,
// so the socket file doesn't exist, and the probe reports
// "ok_direct" on success.
//
// In an RF deployment tailscaled is started before the skygate
// build, so the socket exists by the time the admin hits
// /admin/telegram, and the probe reports "ok_relay" on success.
func isTailscaleActive() bool {
	for _, p := range tailscaleSocketPaths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// resolveTelegramAPI returns the IPs api.telegram.org currently
// resolves to. Used by the probe diagnostic so the operator can
// verify those IPs are in the relay's --advertise-routes list.
//
// The DNS resolution goes through the system resolver (which, on
// the host or in the skygate container, may go through Tailscale's
// MagicDNS). This is fine for the diagnostic: we want to see what
// the bot's HTTP client would resolve to.
func resolveTelegramAPI() []string {
	resolver := &net.Resolver{}
	ips, err := resolver.LookupHost(context.Background(), "api.telegram.org")
	if err != nil {
		return nil
	}
	return ips
}
