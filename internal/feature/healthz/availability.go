// Package healthz — availability.go (v0.33.1.40 B92).
//
// Periodic background check of the integrations skygate depends on
// but does not own: headscale (gRPC API + health endpoint), headplane
// (web UI), and the local Tailscale state. The checker runs every
// 30s in a background goroutine, caches the latest result, and
// exposes it via Snapshot() for both /readyz (the JSON response
// is enriched with `headplane` + `tailscale` fields) and the new
// /admin/services HTML page (the operator-facing status board).
//
// Why a separate background goroutine instead of doing the check
// on every /readyz scrape: a K8s readinessProbe or Prometheus
// blackbox-exporter can scrape /readyz every 1-5s. Pinging
// headscale + headplane that often would be wasted load on the
// control-plane (headscale is a single SQLite/PG file; 1000
// unnecessary pings/min just to detect 'still alive' is silly
// when the page refreshes every 30s anyway). 30s is the
// configured SKYGATE_AVAILABILITY_CHECK_INTERVAL default — it
// matches the typical R3 (live policy / applied snapshot) cadence
// and the user's-eye refresh rate on the /admin/services page.
//
// /readyz semantics change: pre-v0.33.1.40 the readiness probe
// hit headscale synchronously with a 3s timeout. Post-v0.33.1.40
// the probe reads the CACHED status (refreshed every 30s by the
// background goroutine) — /readyz responds in <5ms regardless of
// headscale latency. This is the right tradeoff because a
// headscale outage doesn't mean skygate should be marked unready
// (it can still serve /my/devices, /my/exit-rules, /admin/headscale
// config, etc. — the headscale call just fails for the 30s
// window before the cache refreshes).
//
// On startup, the checker does ONE synchronous check before
// starting the background loop. This means /readyz always has
// real data within 2s of boot (initial check happens before
// the loop), and the loop keeps it fresh afterwards. If the
// initial check fails (headscale not yet reachable, etc.) the
// cache starts at "unknown" state and the UI shows "checking..."
// until the first successful check lands — same UX as a slow
// page load.
package healthz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// IntegrationKind enumerates the external services we probe.
// New integrations = new kind + new check function.
type IntegrationKind string

const (
	IntegrationHeadscale  IntegrationKind = "headscale"
	IntegrationHeadplane  IntegrationKind = "headplane"
	IntegrationTailscale  IntegrationKind = "tailscale"
	IntegrationDatabase   IntegrationKind = "database"
)

// IntegrationStatus is the per-integration probe result. We
// store EVERYTHING in the snapshot (last-checked, latency,
// error string if any, response body for headscale) so the
// /admin/services page can render a useful debug view without
// re-probing on click.
//
// JSON-stable contract: the field names (id, ok, last_checked,
// latency_ms, detail, error) are pinned. The /readyz response
// only exposes id + ok + last_checked; the HTML page reads
// the full struct via Snapshot().
type IntegrationStatus struct {
	ID          IntegrationKind `json:"id"`
	Label       string          `json:"label"`        // human-readable
	URL         string          `json:"url"`          // configured URL (or "(local)")
	OK          bool            `json:"ok"`           // last probe result
	LastChecked time.Time       `json:"last_checked"` // RFC3339; zero if never checked
	LatencyMS   int64           `json:"latency_ms"`   // wall-clock of last probe
	Detail      string          `json:"detail"`       // response body / version / etc.
	Error       string          `json:"error,omitempty"`
}

// IsZero reports whether the integration has never been
// checked (LastChecked is the zero time). The /admin/services
// template uses this to render "checking..." vs "up"/"down".
func (s IntegrationStatus) IsZero() bool {
	return s.LastChecked.IsZero()
}

// Availability is the FULL snapshot of all integration statuses
// at a point in time. Returned by Checker.Snapshot() and embedded
// in the /admin/services page render.
type Availability struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Integrations []IntegrationStatus `json:"integrations"`
}

// AllOK reports whether every checked integration is OK. The
// checker only adds a check to "fail" /readyz if its check ran
// at least once AND the result was NOT ok; "never checked" is
// treated as "skipped" (matches the pre-v0.33.1.40 headscale
// "no headscale configured" semantic).
func (a Availability) AllOK() bool {
	for _, s := range a.Integrations {
		if s.IsZero() {
			continue
		}
		if !s.OK {
			return false
		}
	}
	return true
}

// Find returns the integration status by ID, or false.
func (a Availability) Find(id IntegrationKind) (IntegrationStatus, bool) {
	for _, s := range a.Integrations {
		if s.ID == id {
			return s, true
		}
	}
	return IntegrationStatus{}, false
}

// Checker runs periodic probes and exposes the latest snapshot.
// One Checker per process; constructed at boot by main.go with
// the configured URLs and the check interval (from env or
// default 30s). The background goroutine is started by Start().
//
// Concurrency: state uses atomic.Pointer so /readyz reads are
// lock-free. Start() / Stop() are idempotent.
type Checker struct {
	HeadscaleURL  string
	HeadplaneURL  string
	TailscaleFn   func() (online bool, detail string) // for local node status
	Interval      time.Duration
	httpClient    *http.Client

	snapshot atomic.Pointer[Availability]
	stop     chan struct{}
	running  atomic.Bool
}

// NewChecker constructs the Checker. TailscaleFn is optional
// (nil disables the tailscale check). The Interval is clamped
// to a minimum of 5s to avoid accidental config-driven probe
// storms.
func NewChecker(headscaleURL, headplaneURL string, tailscaleFn func() (bool, string), interval time.Duration) *Checker {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	return &Checker{
		HeadscaleURL: headscaleURL,
		HeadplaneURL: headplaneURL,
		TailscaleFn:  tailscaleFn,
		Interval:     interval,
		// 3s per-probe timeout. headscale and headplane are local
		// (same host or same Tailnet); 3s is plenty.
		httpClient: &http.Client{Timeout: 3 * time.Second},
		stop:       make(chan struct{}),
	}
}

// Start runs the first probe synchronously (so /readyz has
// real data within 3s of boot), then launches the background
// loop. Idempotent: a second call is a no-op.
func (c *Checker) Start(ctx context.Context) {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	// Initial synchronous probe — makes /readyz + /admin/services
	// usable immediately. If this fails, the snapshot stays at
	// zero-state and the UI shows "checking..." until the loop
	// succeeds.
	c.runOnce(ctx)
	go c.loop(ctx)
}

// Stop signals the background loop to exit. Safe to call
// multiple times. The in-flight probe (if any) runs to
// completion because it has its own context.
func (c *Checker) Stop() {
	if c.running.CompareAndSwap(true, false) {
		close(c.stop)
	}
}

// Snapshot returns the latest availability snapshot. Always
// returns a non-nil pointer (zero-value on first call before
// any probe has completed). Lock-free read.
func (c *Checker) Snapshot() *Availability {
	s := c.snapshot.Load()
	if s == nil {
		// Empty snapshot — /readyz treats this as "still checking".
		return &Availability{GeneratedAt: time.Now().UTC()}
	}
	return s
}

// loop is the background goroutine. Runs runOnce() every
// Interval, exits cleanly on stop.
func (c *Checker) loop(ctx context.Context) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			c.runOnce(ctx)
		}
	}
}

// runOnce probes all integrations and stores the snapshot.
// Each integration probe has its own bounded context so a
// slow headscale check doesn't block a quick headplane check.
func (c *Checker) runOnce(parent context.Context) {
	// Defensive: callers should pass a real context, but if
	// they don't (e.g. unit tests), fall back to Background
	// so we still produce a useful snapshot. The 3s per-probe
	// timeout is enforced by the http.Client.Timeout below, so
	// a nil context here just means the loop never sees ctx.Done()
	// — fine for a single-shot probe.
	if parent == nil {
		parent = context.Background()
	}
	// Independent checks; collect results then publish.
	var (
		hs  IntegrationStatus
		hp  IntegrationStatus
		ts  IntegrationStatus
	)
	// headscale (HTTP GET /health)
	hsCtx, hsCancel := context.WithTimeout(parent, 3*time.Second)
	hs = c.checkHeadscale(hsCtx)
	hsCancel()
	// headplane (HTTP GET /)
	hpCtx, hpCancel := context.WithTimeout(parent, 3*time.Second)
	hp = c.checkHeadplane(hpCtx)
	hpCancel()
	// tailscale (local state, no HTTP)
	ts = c.checkTailscale()

	// Merge with previous snapshot for integrations we didn't
	// re-check (preserves LastChecked + LatencyMS across runs
	// when one probe fails and we want to keep the last good
	// record visible). For now, every integration is re-checked
	// each tick, so this is just a list.
	avail := &Availability{
		GeneratedAt:  time.Now().UTC(),
		Integrations: []IntegrationStatus{hs, hp, ts},
	}
	c.snapshot.Store(avail)
}

// checkHeadscale probes HEADSCALE_URL/health. headscale's
// /health returns 200 + {"status":"pass"} once it's ready to
// accept API calls. We do NOT need the API key for /health.
//
// When HEADSCALE_URL is empty (no headscale configured, e.g.
// read-only deploys) we return IsZero=true so the UI shows
// "not configured" and /readyz treats it as "skipped".
func (c *Checker) checkHeadscale(ctx context.Context) IntegrationStatus {
	st := IntegrationStatus{
		ID:    IntegrationHeadscale,
		Label: "Headscale control plane",
		URL:   c.HeadscaleURL,
	}
	if c.HeadscaleURL == "" {
		return st // IsZero=true → "not configured"
	}
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.HeadscaleURL, "/")+"/health", nil)
	if err != nil {
		st.Error = err.Error()
		st.LastChecked = time.Now().UTC()
		return st
	}
	resp, err := c.httpClient.Do(req)
	st.LatencyMS = time.Since(t0).Milliseconds()
	if err != nil {
		st.Error = err.Error()
		st.LastChecked = time.Now().UTC()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		st.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		st.LastChecked = time.Now().UTC()
		return st
	}
	// Read up to 1KB of body for the detail field.
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	st.Detail = strings.TrimSpace(string(buf[:n]))
	st.OK = true
	st.LastChecked = time.Now().UTC()
	return st
}

// checkHeadplane probes HEADPLANE_URL + "/admin/healthz".
// headplane 0.6.x is a distroless Node.js image that does NOT
// expose HTML on `/` (it returns 404) — the only public
// health endpoint is `/admin/healthz` which returns
// `{"status":"OK"}` with 200. The `/` probe in the v0.33.1.40
// (B92) original code worked against the older `ghcr.io/tale/headplane`
// web-UI image but the distroless 0.6.3 image replaced it.
//
// The probe URL is the operator's HEADPLANE_URL + "/admin/healthz"
// (HEADPLANE_URL is the headplane base URL, e.g.
// `http://headplane:50445`). If the operator already included
// `/admin/healthz` in HEADPLANE_URL, this is a no-op
// (`strings.HasSuffix` check prevents double-append).
//
// If headplane is on a different host from headscale (operator
// runs them separately), the URL is read from HEADPLANE_URL
// (which defaults to the same host as HEADSCALE_URL on port
// the operator chooses, e.g. http://headplane:50445).
func (c *Checker) checkHeadplane(ctx context.Context) IntegrationStatus {
	st := IntegrationStatus{
		ID:    IntegrationHeadplane,
		Label: "Headplane admin UI",
		URL:   c.HeadplaneURL,
	}
	if c.HeadplaneURL == "" {
		return st // IsZero=true
	}
	probeURL := c.HeadplaneURL
	if !strings.HasSuffix(probeURL, "/admin/healthz") {
		probeURL = strings.TrimRight(probeURL, "/") + "/admin/healthz"
	}
	t0 := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
	if err != nil {
		st.Error = err.Error()
		st.LastChecked = time.Now().UTC()
		return st
	}
	resp, err := c.httpClient.Do(req)
	st.LatencyMS = time.Since(t0).Milliseconds()
	if err != nil {
		st.Error = err.Error()
		st.LastChecked = time.Now().UTC()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		st.Error = fmt.Sprintf("HTTP %d (probed %s)", resp.StatusCode, probeURL)
		st.LastChecked = time.Now().UTC()
		return st
	}
	st.Detail = fmt.Sprintf("HTTP %d (probed %s)", resp.StatusCode, probeURL)
	st.OK = true
	st.LastChecked = time.Now().UTC()
	return st
}

// checkTailscale inspects the local tailscaled state. The
// detail string is whatever the caller puts there (typically
// "online (100.64.0.18)" or "offline"). When the caller didn't
// wire TailscaleFn (Tailscale disabled in compose), IsZero=true
// so the UI shows "not configured" and /readyz doesn't flag it.
func (c *Checker) checkTailscale() IntegrationStatus {
	st := IntegrationStatus{
		ID:    IntegrationTailscale,
		Label: "Tailscale node (this skygate host)",
		URL:   "(local tailscaled)",
	}
	if c.TailscaleFn == nil {
		return st
	}
	t0 := time.Now()
	online, detail := c.TailscaleFn()
	st.LatencyMS = time.Since(t0).Milliseconds()
	st.OK = online
	st.Detail = detail
	st.LastChecked = time.Now().UTC()
	if !online {
		st.Error = detail
	}
	return st
}

// envInt reads an env var as int (seconds), returning the
// default when unset or unparseable. Used for
// SKYGATE_AVAILABILITY_CHECK_INTERVAL (in seconds — operators
// think in seconds, not Duration).
func envInt(name string, defSec int) int {
	v := os.Getenv(name)
	if v == "" {
		return defSec
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return defSec
	}
	return n
}

// DefaultCheckInterval is the fallback when SKYGATE_AVAILABILITY_CHECK_INTERVAL
// is unset or invalid. 30s matches the typical operator
// page-refresh rate and is conservative on the control plane.
const DefaultCheckInterval = 30 * time.Second

// NewCheckerFromEnv constructs a Checker from the standard
// SKYGATE_* env vars. The TailscaleFn is nil by default —
// the caller (main.go) wires it after constructing the
// *headscale.Client. The HeadscaleURL is required (operators
// without headscale shouldn't enable this checker). The
// HeadplaneURL defaults to http://<headscale-host>:8080 if
// unset.
//
// SKYGATE_AVAILABILITY_CHECK_INTERVAL — check period in seconds
// (default 30, min 5, max 300).
func NewCheckerFromEnv(headscaleURL, headplaneURL string, tailscaleFn func() (bool, string)) *Checker {
	sec := envInt("SKYGATE_AVAILABILITY_CHECK_INTERVAL", int(DefaultCheckInterval.Seconds()))
	return NewChecker(headscaleURL, headplaneURL, tailscaleFn, time.Duration(sec)*time.Second)
}

// JSON serializes the snapshot to JSON. Used by /readyz and
// the /admin/services page (via a fetch).
func (a *Availability) JSON() string {
	b, _ := json.Marshal(a)
	return string(b)
}
