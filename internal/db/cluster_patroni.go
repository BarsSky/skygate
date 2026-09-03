// Package db — cluster_patroni.go owns the Patroni
// /switchover /failover helpers. Phase 3.3 of
// docs/internal/cluster-management.md.
//
// Background
// ----------
// Patroni is the PG HA layer. Each PG node runs a
// Patroni daemon exposing a REST API on :8008. Patroni
// handles automatic leader election + replica promotion
// via etcd; skygate's role (per the plan) is to surface
// Patroni's SWITCHOVER capability through the admin
// UI and let the operator trigger it explicitly.
//
// Why an explicit "switchover" button?
// -----------------------------------
// Patroni's automatic failover (when the current leader
// is unreachable) is the "failover" case — Patroni
// picks the best replica and promotes it. The
// "switchover" case is operator-driven: the current
// leader is healthy, but the operator wants a planned
// swap (maintenance, hardware move, balancing).
// Patroni exposes both via POST /switchover and
// POST /failover; we wrap /switchover here because
// (a) it's the safer default (rejects unhealthy
// leaders), (b) the plan says "Patroni is already
// in place, just plumb to UI" — the operator's
// "failover" intent maps to /switchover in healthy
// cases, and the auto-failover in unhealthy cases
// happens without skygate being in the loop.
//
// Phase 3.7 (auto-rollback) is the future work for
// the unhealthy case — skygate will detect a failed
// promotion and either retry or revert. Phase 3.3
// is just the operator-triggered happy path.
//
// Patroni REST contract
// ----------------------
// POST {patroni_url}/switchover
//   body: {"leader": "<current leader name>",
//          "candidate": "<target leader name>",
//          "scheduled_at": "<optional ISO timestamp>"}
//   response: 200 + JSON {"succeeded": true,
//                          "switchover_timestamp": "..."}
//            or 4xx/5xx + error JSON.
//
// We do a synchronous switchover (no scheduled_at) so
// the operator gets the result back in the HTTP
// response. The watchdog (B210) detects the new DSN
// from etcd + hot-reloads the pgxpool — skygate
// keeps running on the new primary without restart.
//
// What we don't do
// ----------------
// - No scheduled / deferred switchover (Patroni supports
//   `scheduled_at` but that's a Phase 3.7 enhancement
//   for planned maintenance windows).
// - No multi-region considerations (Patroni itself
//   handles that via DCS quorum; skygate just calls
//   the local REST API).
// - No automatic candidate selection (the operator
//   types the candidate name; Patroni's automatic
//   failover does the unhealthy-case candidate pick
//   for us out-of-band).

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FailoverDBSwitchoverResult is the parsed response
// from Patroni's POST /switchover endpoint. The
// "scheduled_at" field is optional — Patroni sets it
// when the switchover was scheduled (we don't use
// that path; we always do a synchronous switchover).
type FailoverDBSwitchoverResult struct {
	Succeeded           bool   `json:"succeeded"`
	SwitchoverTimestamp string `json:"switchover_timestamp,omitempty"`
	// Patroni error responses use a "err" or
	// "error" key (varies by version). We capture
	// both for forward-compat.
	Err  string `json:"err,omitempty"`
	Error string `json:"error,omitempty"`
}

// FailoverDB calls Patroni's POST /switchover endpoint
// to promote `candidate` to primary (demoting the
// current leader, which Patroni identifies from its
// own state). Synchronous — returns when Patroni
// reports the switchover as complete.
//
// `patroniURL` is the base URL of the current leader's
// Patroni (e.g. "http://10.0.0.5:8008"). The caller
// chooses which Patroni to call — usually the
// current leader (the watchdog knows via /patroni
// state), but in degraded scenarios the operator
// might point at a replica's Patroni (Patroni
// rejects the switchover if it's not the leader's
// REST API, returning a clear error).
//
// `reason` is a free-text string stored in the
// cluster_audit row (the operator's intent, e.g.
// "planned maintenance").
//
// Returns the Patroni response (parsed) on HTTP
// 200 + body parse OK. Returns an error for:
//   - HTTP non-200 (Patroni rejected the request)
//   - HTTP 200 but body parse failed
//   - HTTP 200 but succeeded=false (Patroni
//     acknowledged but reported a problem)
//   - Patroni body has err/error field set
func FailoverDB(ctx context.Context, patroniURL, leader, candidate, reason string) (*FailoverDBSwitchoverResult, error) {
	if patroniURL == "" {
		return nil, fmt.Errorf("cluster: empty patroni URL — set SKYGATE_PATRONI_URL or pass explicitly")
	}
	if candidate == "" {
		return nil, fmt.Errorf("cluster: empty candidate name")
	}
	// Default leader to empty — Patroni will pick
	// the current leader from its own state. The
	// caller can override for explicitness.
	body := map[string]interface{}{
		"candidate": candidate,
	}
	if leader != "" {
		body["leader"] = leader
	}
	if reason != "" {
		// Patroni logs the reason in its own audit
		// log; we don't pass it as a body field
		// (Patroni doesn't accept custom reason
		// fields — it just runs the switchover).
		// We use reason only in our cluster_audit
		// row on the skygate side.
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cluster: marshal body: %w", err)
	}
	url := strings.TrimRight(patroniURL, "/") + "/switchover"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("cluster: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 60 * time.Second} // switchover can take a few seconds
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cluster: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cluster: Patroni returned %d: %s", resp.StatusCode, truncateBody(respBody, 200))
	}
	var result FailoverDBSwitchoverResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("cluster: parse Patroni response: %w (raw: %s)", err, truncateBody(respBody, 200))
	}
	if !result.Succeeded {
		// Patroni 200 but succeeded=false — happens
		// for race conditions (e.g. another switchover
		// completed between our POST and Patroni's
		// response). Surface the error.
		msg := result.Err
		if msg == "" {
			msg = result.Error
		}
		if msg == "" {
			msg = "Patroni reported succeeded=false (no error message in body)"
		}
		return &result, fmt.Errorf("cluster: Patroni switchover did not succeed: %s", msg)
	}
	return &result, nil
}

// truncateBody caps the size of the raw body we embed
// in error messages (a 50KB Patroni error traceback
// would explode our log line; 200 bytes is enough to
// see "what went wrong" without the noise).
func truncateBody(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
