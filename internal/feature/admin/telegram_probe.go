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
)

// String renders the state as a stable lower-case identifier
// used in the template (for the CSS class hook, e.g.
// .probe-ok-relay).
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

// TelegramProbeResult is what the probe returns.
type TelegramProbeResult struct {
	State       TelegramProbeState
	Message     string
	Latency     time.Duration
	LatencyMS   string
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
			State:     ProbeUnreachable,
			Message:   "build request: " + err.Error(),
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
			Message:   err.Error(),
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
