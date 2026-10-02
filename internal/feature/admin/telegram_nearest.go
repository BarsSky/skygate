// telegram_nearest.go — "nearest egress" selection.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): the handler, the
// injectable `tailscale status --json` runner and the peer-latency parse it feeds.

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
)

// handleTelegramSetNearestEgress (B255, 2026-09-16) is the
// one-click "Pin nearest exit node" button.
//
// It enumerates the enabled exit_servers, reads
// `tailscale status --json` on the host (skygate-host-1) to
// pick up each peer's `PeerLatency`, sorts by latency ascending
// and applies the canonical Telegram-CIDR to the FASTEST one —
// the same SSH+advertise-routes code path as
// handleTelegramSetEgress. If the host's tailscaled isn't
// running (no PeerLatency data) the operator gets a clear
// flash pointing at /admin/tailscale (Start), and we fall
// back to the FIRST enabled relay so the button still does
// something useful (the operator can manually pick another
// via the dropdown above).
func (s *Service) handleTelegramSetNearestEgress(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	lang := s.I18n.LangFromRequest(r)
	auditUID, auditName := s.Backend.InfraAuditIdentity(c.UserID, c.Username)
	relays, err := db.ListExitServers(s.dbc())
	if err != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			fmt.Sprintf("err=%q", err.Error()))
		writeErrRedirect(w, r, i18n.Tf(lang, "telegram.egress_apply_fail", err.Error()))
		return
	}
	enabled := make([]db.ExitServer, 0, len(relays))
	for _, e := range relays {
		if e.Enabled {
			enabled = append(enabled, e)
		}
	}
	if len(enabled) == 0 {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			"no_enabled_relays")
		writeErrRedirect(w, r, i18n.T(lang, "telegram.egress_no_relays"))
		return
	}

	// Measure latency from skygate-host's tailscaled. If
	// tailscaled isn't running the PeerLatency map is empty —
	// fall back to the first enabled relay (sorted by node_id
	// for stability) so the click still applies SOMETHING and
	// the operator gets an actionable flash.
	latencies, latErr := tailscalePeerLatencies()
	if latErr != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			fmt.Sprintf("latency_err=%q fallback=first", latErr.Error()))
	}
	// Sort the enabled relays by latency ascending; relays with
	// no latency measurement land at the bottom so they're only
	// picked if NO relay has a measurement.
	sort.SliceStable(enabled, func(i, j int) bool {
		li, oki := latencies[enabled[i].Hostname]
		lj, okj := latencies[enabled[j].Hostname]
		if oki != okj {
			return oki // measured relays first
		}
		if !oki {
			return false // both unmeasured — preserve ListExitServers order
		}
		return li < lj
	})

	picked := enabled[0]
	s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
		fmt.Sprintf("picked=%s latency_ms=%s candidates=%d",
			picked.Hostname,
			strconv.FormatFloat(latencies[picked.Hostname], 'f', 1, 64),
			len(enabled)))

	// Apply via the shared helper. We rewrite r.FormValue
	// by injecting the picked node_id into r.PostForm so the
	// applyEgress helper picks it up. (handleTelegramSetEgress
	// already does the SSH + advertise-routes + audit work;
	// we reuse it to avoid duplicating 80 lines of code.)
	r.PostForm.Set("node_id", picked.NodeID)
	r.Form.Set("node_id", picked.NodeID)
	s.handleTelegramSetEgress(w, r, c)
}

// tailscaleStatusExecFn is the indirection that unit tests
// override to stub the `tailscale status --json` output
// without running tailscale on the host. Production code
// calls tailscalePeerLatencies; tests use this var to
// return canned JSON. The ctx is passed through so the
// real implementation honours the 5s timeout (tests
// typically ignore it).
var tailscaleStatusExecFn = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
}

// tailscalePeerLatencies (B255) returns a map of
// headscale `HostName` → round-trip latency in milliseconds,
// derived from `tailscale status --json` on the HOST
// (skygate-host-1). Returns (empty, nil) when tailscaled
// isn't running — callers treat that as "no measurement
// available" rather than a hard error.
//
// Why "on host" instead of inside the skygate container:
// the latency we care about is the routing latency from
// the host that runs skygate to the relay — that's the
// path the Telegram-CIDR traffic will actually take. The
// container's view is its OWN tailscaled peer graph, which
// is a different (often higher-latency) path.
//
// The status JSON exposes PeerLatency as either a number
// (the legacy "ms" form) or an object { "ms": N, ... }
// (newer clients). We accept both.
func tailscalePeerLatencies() (map[string]float64, error) {
	out := map[string]float64{}
	if !tailscaledRunning() {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := tailscaleStatusExecFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	// Use json.Number via decoder so we don't lose precision
	// on the latency field. The shape is:
	//   "Peer": { "<nodekey>": { "HostName": "...", "PeerLatency": { "ms": 12.3 } } }
	// but legacy tailscale clients emit "PeerLatency": 12.3
	// (number, not object). We accept both via a custom
	// unmarshal hook on a wrapper type.
	var decoded struct {
		Peer map[string]struct {
			HostName    string          `json:"HostName"`
			PeerLatency json.RawMessage `json:"PeerLatency"`
		} `json:"Peer"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	for _, p := range decoded.Peer {
		if p.HostName == "" || len(p.PeerLatency) == 0 {
			continue
		}
		var ms float64
		// Try the object form first: {"ms": 12.3, ...}
		var obj struct {
			MS json.Number `json:"ms"`
		}
		if err := json.Unmarshal(p.PeerLatency, &obj); err == nil && obj.MS != "" {
			if f, ferr := strconv.ParseFloat(string(obj.MS), 64); ferr == nil {
				ms = f
			}
		}
		if ms == 0 {
			// Try the legacy bare-number form: 12.3
			if f, ferr := strconv.ParseFloat(string(p.PeerLatency), 64); ferr == nil {
				ms = f
			}
		}
		if ms > 0 {
			out[p.HostName] = ms
		}
	}
	return out, nil
}
