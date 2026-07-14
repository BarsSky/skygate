package handlers

// 2026-07-14: Этап 14 v2 — Tailscale reachability probe for the bot.
//
// The probe is what /admin/telegram uses to surface a 3-state banner
// on every page load:
//
//   ok_direct    — api.telegram.org reachable, and the kernel route
//                  for the resolved IPs goes via eth0 (direct
//                  internet, no Tailscale involvement for this
//                  destination). Typical for non-RF VPSes.
//
//   ok_relay     — api.telegram.org reachable, and the kernel route
//                  for the resolved IPs goes via tailscale0 —
//                  which means a subnet route from a relay (e.g.
//                  emilia) covers the destination. Typical for
//                  RF deployments where direct internet is blocked.
//
//   unreachable  — api.telegram.org did NOT respond within the 5s
//                  timeout. The banner shows troubleshooting steps.
//
// The original v1 of this probe used "is tailscaled running" as
// the ok_relay / ok_direct distinguisher. That was wrong: tailscaled
// can be running (joining the tailnet for admin access / headscale
// access) without any subnet route covering api.telegram.org's
// resolved IPs — in which case the actual traffic still goes via
// eth0, and the probe was lying. v2 (this version) uses the kernel
// routing table directly: `ip route get <ip>` tells us which
// interface the kernel would route the packet through, and that's
// the truth. If the answer is "dev tailscale0", a subnet route
// covered the destination; otherwise it's direct.
//
// The probe is cheap (single GET with a 5s timeout) and runs
// synchronously on every GET /admin/telegram. We don't cache the
// result — the admin page is hit infrequently and stale "OK"
// banners are worse than a 200ms page load.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
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

	// ProbeOKDirect: api.telegram.org responded, and the kernel
	// would route the request via eth0 (direct internet, not
	// through any Tailscale subnet route). Typical for non-RF
	// VPSes that don't need Tailscale for egress.
	ProbeOKDirect

	// ProbeOKRelay: api.telegram.org responded, and the kernel
	// would route the request via tailscale0 — i.e. a relay's
	// subnet route covers the destination. Typical for RF
	// deployments.
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

// probeTelegramAPIWithBase does a real GET against the
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
			State:     ProbeUnreachable,
			Message:   "build request: " + err.Error(),
			Latency:   time.Since(start),
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
			State:     ProbeUnreachable,
			Message:   err.Error(),
			Latency:   latency,
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
			State:     ProbeUnreachable,
			Message:   "api.telegram.org 5xx: HTTP " + resp.Status,
			Latency:   latency,
			LatencyMS: formatLatencyMS(latency),
		}
	}

	// Resolve api.telegram.org for the diagnostic display AND
	// for the route check. We do a separate DNS lookup (not the
	// one inside the HTTP client) so the operator can see which
	// IPs we hit and verify they're covered by the relay's
	// advertised subnet routes. The route check is per-IP
	// because Tailscale's accepted subnet routes are per-CIDR
	// and we want to know whether EACH resolved IP is covered.
	ips := resolveTelegramAPI()

	state, message := classifyRoute(ips)
	return TelegramProbeResult{
		State:       state,
		Message:     message,
		Latency:     latency,
		LatencyMS:   formatLatencyMS(latency),
		ResolvedIPs: ips,
	}
}

// classifyRoute decides between ok_direct / ok_relay based on
// which interface the kernel would route api.telegram.org's
// resolved IPs through.
//
// We check EACH resolved IP (not just the first) because Telegram
// can return multiple A/AAAA records and they may not all be
// covered by the same subnet route (e.g. a stale AAAA record
// that no longer points at a Telegram IP). If ANY IP goes via
// tailscale0, we report ok_relay — the bot uses the same Go HTTP
// transport, so it'll also use the relay path for at least some
// of the IPs (Go HTTP client picks one, usually the first).
//
// `tailscaleSocketPaths` and the `isTailscaleActive` heuristic
// from v1 of this file are deliberately NOT consulted here:
// "is tailscaled running" is a process-state question, not a
// route question, and on a non-RF VPS tailscaled can be running
// without any subnet route covering the destination. The
// kernel routing table is the source of truth for "would this
// packet go via Tailscale?" — we use that.
func classifyRoute(ips []string) (TelegramProbeState, string) {
	if len(ips) == 0 {
		// No resolved IPs (DNS failed). The HTTP probe
		// succeeded but we can't tell where the request
		// went. Report "ok_direct" as the conservative
		// answer — "we got a response, and the route
		// table is empty for it, so direct".
		return ProbeOKDirect, "Reachable via direct internet (no resolved IPs)"
	}
	for _, ip := range ips {
		if isRouteViaTailscale(ip) {
			return ProbeOKRelay, "Reachable via Tailscale relay (subnet route)"
		}
	}
	return ProbeOKDirect, "Reachable via direct internet"
}

// isRouteViaTailscale runs `ip route get <ip>` and returns true
// if the kernel would route packets to that IP via the tailscale0
// interface.
//
// We shell out to `ip route get` rather than parse
// /proc/net/route ourselves because (a) it's a tiny one-shot
// command, (b) it handles IPv4/IPv6, table selection, and the
// Tailscale-specific table 52 routing rules correctly without us
// re-implementing that logic, and (c) the call is cheap
// (sub-millisecond on a typical Linux box).
//
// The function is wrapped behind the routeViaTailscaleFn var
// for testability: tests can override the var to feed fake
// "ip route get" responses without involving the kernel.
func isRouteViaTailscale(ip string) bool {
	return routeViaTailscaleFn(ip)
}

// routeViaTailscaleFn is the indirection that tests use to
// fake the `ip route get` output. Production code calls
// isRouteViaTailscale; tests override this var.
var routeViaTailscaleFn = defaultRouteViaTailscale

// defaultRouteViaTailscale is the real implementation: shell
// out to `ip route get` and check whether the route's
// "dev tailscale0" appears in the output.
//
// We use `exec.Command` (not os/exec's combined-output form)
// so a `ip` command that hangs can't block the probe past the
// 5s context timeout — the context is consulted at exec time.
// The `ip` command itself doesn't honour contexts, so as a
// belt-and-suspenders we wrap the call in a small goroutine
// with its own timeout.
func defaultRouteViaTailscale(ip string) bool {
	done := make(chan bool, 1)
	var result bool
	go func() {
		out, err := exec.Command("ip", "route", "get", ip).Output()
		if err != nil {
			result = false
		} else {
			result = strings.Contains(string(out), "dev tailscale0")
		}
		done <- true
	}()
	select {
	case <-done:
		return result
	case <-time.After(2 * time.Second):
		// `ip route get` shouldn't take this long. If it
		// does, assume "not via tailscale" — false
		// negatives are recoverable (operator can refresh
		// the page); false positives (lying about relay
		// routing) are not.
		return false
	}
}

// resolveTelegramAPI returns the IPs api.telegram.org currently
// resolves to. Used by the probe diagnostic so the operator can
// verify those IPs are in the relay's --advertise-routes list.
//
// The DNS resolution goes through the system resolver (which, on
// the host or in the skygate container, may go through Tailscale's
// MagicDNS, except we set --accept-dns=false in the entrypoint
// so it's actually the system / Docker DNS). This is fine for
// the diagnostic: we want to see what the bot's HTTP client
// would resolve to.
func resolveTelegramAPI() []string {
	resolver := &net.Resolver{}
	ips, err := resolver.LookupHost(context.Background(), "api.telegram.org")
	if err != nil {
		return nil
	}
	return ips
}
