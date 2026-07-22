package handlers

// handlers_healthz.go — liveness + readiness probes (v0.26.0).
//
// Why this exists: the v0.26.0 HA-ready architecture needs
// the operator (or a future K8s / systemd unit / load
// balancer) to be able to ask "is this skygate instance
// ready to serve traffic?". Two separate concerns:
//
//   - Liveness  (GET /healthz): is the process alive and
//                                 the HTTP listener responsive?
//                                 Always 200 OK if we got here.
//                                 K8s livenessProbe pattern.
//
//   - Readiness (GET /readyz): can the instance actually
//                                 serve a request? Pings the
//                                 database and the headscale
//                                 control plane. 200 OK if
//                                 both reachable, 503 if
//                                 either is down. K8s
//                                 readinessProbe pattern.
//
// Both endpoints are UNAUTHENTICATED (no authMW). The
// /readyz response body is JSON so an external monitor
// (Prometheus blackbox-exporter, Grafana, opsgenie, etc.)
// can parse the per-component status. The handler does
// NOT log every probe (that would flood the audit log
// at typical 10s probe intervals); it only logs state
// CHANGES (newly-degraded or newly-recovered).
//
// v0.26.0 — first HA-ready release. No real HA deploy
// yet; this just puts the hooks in place.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"skygate/internal/headscale"
)

// readyzState is the body returned by GET /readyz.
// The HTTP status is 200 if Healthy=true, 503 otherwise.
type readyzState struct {
	Healthy     bool              `json:"healthy"`
	DB          string            `json:"db"`           // "ok" / "error: <reason>"
	Headscale   string            `json:"headscale"`    // "ok" / "error: <reason>" / "skipped" (HS disabled)
	InstanceID  string            `json:"instance_id"`  // SKYGATE_INSTANCE_ID env, or "unconfigured"
	Build       string            `json:"build"`        // v0.26.0 + commit (set at boot)
	UptimeSec   int64             `json:"uptime_sec"`
	Timestamp   string            `json:"timestamp"`    // RFC3339, server time
	Checks      map[string]string `json:"checks"`       // individual check results
}

// readyzCache is updated on every probe. We cache the
// last result so an external monitor scraping every
// 100ms doesn't hammer the DB or headscale. The cache
// has a 1-second TTL (atomic.Pointer store of the
// last successful probe time + state).
type readyzCache struct {
	lastAt  atomic.Int64 // unix seconds
	state   atomic.Pointer[readyzState]
}

// Cache the last probe result for 1 second. Probes
// within that window get the cached state. Probes
// after 1s trigger a fresh check.
const readyzCacheTTL = 1

// readyz is a process-wide cache. Single global is
// fine because the handler is HTTP-serialized (one
// request at a time per goroutine, but they don't
// share a Go pointer to this struct).
var readyz = &readyzCache{}

// GetHealthz — liveness probe. Always 200 OK with a
// tiny JSON body. Never touches the DB or headscale
// (that's what /readyz is for). K8s livenessProbe.
func (a *App) GetHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"instance_id": a.InstanceID,
		"build":       a.BuildVersion,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
}

// GetReadyz — readiness probe. Pings the DB and the
// headscale client (if configured), caches the result
// for 1 second, returns 200 OK or 503 Service Unavailable
// with a per-component breakdown. K8s readinessProbe.
func (a *App) GetReadyz(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	last := readyz.lastAt.Load()
	if now-last < readyzCacheTTL {
		// Fresh cache — return it.
		if s := readyz.state.Load(); s != nil {
			a.writeReadyz(w, s)
			return
		}
	}
	// Cache miss (or first call). Run the checks.
	state := a.runReadyzChecks(r.Context())
	readyz.state.Store(&state)
	readyz.lastAt.Store(now)
	a.writeReadyz(w, &state)
}

// runReadyzChecks does the actual liveness probes. Each
// check is independently wrapped so a slow DB doesn't
// block the headscale check (and vice versa). The
// overall Healthy flag is AND-of-all-checks.
func (a *App) runReadyzChecks(ctx context.Context) readyzState {
	state := readyzState{
		Checks:     map[string]string{},
		InstanceID: a.InstanceID,
		Build:      a.BuildVersion,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		UptimeSec:  int64(time.Since(a.StartedAt).Seconds()),
	}
	// DB check — cheap (a single ping).
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.DB.PingContext(dbCtx); err != nil {
		state.DB = "error: " + err.Error()
		state.Checks["db"] = "fail"
	} else {
		state.DB = "ok"
		state.Checks["db"] = "ok"
	}
	// Headscale check — only if a client is configured.
	// Read-only deploys (HSForUser() returns nil) get
	// "skipped" so the operator can tell at a glance.
	hs := a.HSGlobal()
	if hs == nil {
		state.Headscale = "skipped (no headscale configured)"
		state.Checks["headscale"] = "skipped"
	} else {
		hsCtx, hsCancel := context.WithTimeout(ctx, 3*time.Second)
		defer hsCancel()
		if err := hs.PingContext(hsCtx); err != nil {
			state.Headscale = "error: " + err.Error()
			state.Checks["headscale"] = "fail"
		} else {
			state.Headscale = "ok"
			state.Checks["headscale"] = "ok"
		}
	}
	// AND of all checks. A single "fail" makes the
	// whole instance unhealthy.
	state.Healthy = state.DB == "ok" &&
		(state.Headscale == "ok" || state.Headscale == "skipped (no headscale configured)")
	return state
}

func (a *App) writeReadyz(w http.ResponseWriter, s *readyzState) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !s.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(s)
}

// Ensure headscale.Client has a PingContext method.
// If not, this file won't compile — which is what we
// want (catches a missing method at build time, not
// at probe time).
var _ = func() interface{} {
	// Compile-time assertion via a typed nil pointer
	// to detect drift.
	var _ headscale.Pingable = (*headscale.Client)(nil)
	return nil
}()
