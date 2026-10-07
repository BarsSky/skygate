// Package admin — telegram_probe.go is the Telegram reachability
// probe (Tailscale-aware). Pure helper, no *App dependency.
// refactor-v0.30 Phase B step 3b.1a (2026-07-29): moved from
// internal/handlers/handlers_telegram_probe.go.

package admin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"skygate/internal/telegram"
)

// TelegramProbeState is the discrete outcome of a probe. The
// integer values are stored in the DB / rendered in the template,
// so they are part of the wire format — do NOT renumber.
type TelegramProbeState int

const (
	// ProbeUnreachable: api.telegram.org did not respond within
	// the timeout (5s).
	ProbeUnreachable TelegramProbeState = iota

	// ProbeOKDirect: api.telegram.org responded, and the
	// kernel would route the request via eth0 (direct internet,
	// not through any Tailscale subnet route).
	ProbeOKDirect

	// ProbeOKRelay: api.telegram.org responded, and the kernel
	// would route the request via tailscale0 — a relay's
	// subnet route covers the destination.
	ProbeOKRelay

	// ProbeOKRelayTunnel (B356.1, 2026-10-07): the BOT ITSELF is reaching
	// api.telegram.org through an SSH tunnel to a peer relay, because the portal's
	// own egress to the API is blocked while the relay can reach it. This is a
	// DIFFERENT fact from ProbeOKRelay: that one is a kernel subnet route
	// (`ip route get … dev tailscale0`) which, on the live incident deployment,
	// never applied even though three relays were reachable over the tailnet.
	// The direct probe on this page measures the DIRECT path on purpose, so it can
	// legitimately fail while the bot works — without this state the page would
	// claim "unreachable" while every notification is being delivered.
	//
	// Kept LAST in the iota block: the numeric values are part of the rendered
	// wire format, so an existing state must never be renumbered.
	ProbeOKRelayTunnel
)

// Healthy reports whether the state means "the Bot API path works". Used by the
// probe cache and the POST handler instead of repeating the state list.
func (s TelegramProbeState) Healthy() bool {
	return s == ProbeOKDirect || s == ProbeOKRelay || s == ProbeOKRelayTunnel
}

// String renders the state as a stable lower-case identifier
// used in the template (for the CSS class hook, e.g.
// .probe-ok-relay).
func (s TelegramProbeState) String() string {
	switch s {
	case ProbeOKDirect:
		return "ok_direct"
	case ProbeOKRelay:
		return "ok_relay"
	case ProbeOKRelayTunnel:
		return "ok_relay_tunnel"
	default:
		return "unreachable"
	}
}

// TelegramProbeResult is what the probe returns.
type TelegramProbeResult struct {
	State       TelegramProbeState
	Message     string
	Latency     time.Duration
	LatencyMS   string
	ResolvedIPs []string
	// Stale marks a result served from the cache by the B253
	// stale-while-revalidate path: it is real data, but older than the TTL,
	// and a background refresh is already in flight. The page shows the
	// "stale" badge so the operator knows the number is not fresh.
	// StaleAt is the RFC3339 timestamp of when that stale result was
	// actually measured (empty when Stale is false).
	//
	// (Kept as one comment block above the pair on purpose: gofmt aligns
	// consecutive field lines, and scripts/check_b253_telegram_async.sh pins
	// the `Stale   bool` alignment.)
	Stale   bool
	StaleAt string
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
func probeTelegramAPI(ctx context.Context, token string) TelegramProbeResult {
	return probeTelegramAPIWithBase(ctx, token, "https://api.telegram.org")
}

func probeTelegramAPIWithBase(ctx context.Context, token, apiBase string) TelegramProbeResult {
	start := time.Now()
	if token == "" {
		return TelegramProbeResult{
			State:   ProbeUnreachable,
			Message: "Telegram bot token not configured — save one to enable the probe",
		}
	}
	endpoint := strings.TrimRight(apiBase, "/") + "/bot" + token + "/getMe"

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TelegramProbeResult{
			State: ProbeUnreachable,
			// B356.1: the endpoint embeds the bot token and net/http quotes the
			// URL in its error — the page and the journal must never carry it.
			Message:   "build request: " + telegram.RedactToken(err.Error(), token),
			Latency:   time.Since(start),
			LatencyMS: formatLatencyMS(time.Since(start)),
		}
	}
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return TelegramProbeResult{
			State:     ProbeUnreachable,
			Message:   telegram.RedactToken(err.Error(), token),
			Latency:   latency,
			LatencyMS: formatLatencyMS(latency),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusInternalServerError {
		return TelegramProbeResult{
			State:     ProbeUnreachable,
			Message:   "api.telegram.org 5xx: HTTP " + resp.Status,
			Latency:   latency,
			LatencyMS: formatLatencyMS(latency),
		}
	}

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
func classifyRoute(ips []string) (TelegramProbeState, string) {
	if len(ips) == 0 {
		return ProbeOKDirect, "Reachable via direct internet (no resolved IPs)"
	}
	for _, ip := range ips {
		if isRouteViaTailscale(ip) {
			return ProbeOKRelay, "Reachable via Tailscale relay (subnet route)"
		}
	}
	return ProbeOKDirect, "Reachable via direct internet"
}

func isRouteViaTailscale(ip string) bool {
	return routeViaTailscaleFn(ip)
}

// routeViaTailscaleFn is the indirection that tests use to
// fake the `ip route get` output. Production code calls
// isRouteViaTailscale; tests override this var.
var routeViaTailscaleFn = defaultRouteViaTailscale

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
		return false
	}
}

// resolveTelegramAPI returns the IPs api.telegram.org currently
// resolves to.
func resolveTelegramAPI() []string {
	resolver := &net.Resolver{}
	ips, err := resolver.LookupHost(context.Background(), "api.telegram.org")
	if err != nil {
		return nil
	}
	return ips
}

// reconcileProbeWithEgress folds the LIVE egress state of the bot's own HTTP client
// (telegram.EgressSnapshot, B356.1) into the direct probe result.
//
// WHY THIS EXISTS. The probe above measures the DIRECT path on purpose — that is what
// tells the operator whether the network they are looking at is the problem. But the
// bot does not use the direct path when the B356.1 fallback is carrying it, so a page
// that renders "unreachable" while every notification is being delivered is exactly the
// silent-degradation class this project keeps paying for. The honest answer is a state
// of its own, `ok_relay_tunnel`, naming the relay and why the direct path failed.
//
// Direction of the override, deliberately one-way:
//   - the direct probe FAILED and the bot is being carried by a relay → ok_relay_tunnel;
//   - the direct probe FAILED and the fallback has candidates but has not been used yet
//     → stays unreachable, with the candidates named (the bot's next call will try them);
//   - the direct probe FAILED and the fallback is switched off → stays unreachable, and
//     SAYS the switch is off, because that is the operator's own configuration;
//   - the direct probe SUCCEEDED → the probe wins (it is the fresher fact); a stale
//     "last call went through the relay" is reported as a note, not as the state.
//
// Pure (unit-tested) so the page's vocabulary can be pinned without a live deployment.
func reconcileProbeWithEgress(res TelegramProbeResult, snap telegram.EgressSnapshot) TelegramProbeResult {
	if !snap.Known {
		// No fallback information at all (no notifier wired, or a NoopNotifier):
		// render exactly what the probe measured, as before B356.1.
		return res
	}
	if res.State.Healthy() {
		if snap.Relay != "" {
			res.Message = joinProbeNotes(res.Message, "the last Bot API call was carried by the relay "+
				snap.Relay+" (SSH tunnel); the direct path is re-probed within a minute")
		}
		return res
	}
	if snap.Relay != "" {
		res.State = ProbeOKRelayTunnel
		msg := "Reachable through the relay " + snap.Relay + " (SSH tunnel)"
		if snap.RelayEndpoint != "" {
			msg += " → " + snap.RelayEndpoint
		}
		if snap.DirectError != "" {
			msg += "; the direct path failed: " + snap.DirectError
		}
		res.Message = msg
		return res
	}
	if !snap.Enabled {
		res.Message = joinProbeNotes(res.Message, "the relay fallback is switched off ("+
			telegram.EnvRelayFallback+"=0), so nothing is being tried on the relay path")
		return res
	}
	if len(snap.Candidates) > 0 {
		res.Message = joinProbeNotes(res.Message, "the relay fallback is armed and will be tried on the next Bot API call (candidates: "+
			strings.Join(snap.Candidates, ", ")+")")
		return res
	}
	if len(snap.Skipped) > 0 {
		res.Message = joinProbeNotes(res.Message, "no relay can carry the Bot API traffic: "+strings.Join(snap.Skipped, "; "))
	}
	return res
}

// joinProbeNotes appends a note to a probe message without doubling separators.
func joinProbeNotes(msg, note string) string {
	if strings.TrimSpace(note) == "" {
		return msg
	}
	if strings.TrimSpace(msg) == "" {
		return note
	}
	return msg + " — " + note
}
