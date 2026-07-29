package healthz

// service.go — the Service struct + HTTP handlers. Moved here
// from internal/handlers/handlers_healthz.go as part of the
// refactor-v0.30 Phase B step 1 (2026-07-29). The Service
// takes its dependencies as plain fields (DB, HeadscaleFn, etc.)
// instead of methods on *App, so this package has no import
// dependency on internal/handlers/.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"skygate/internal/headscale"
)

// Service is the healthz feature module. One Service is created
// at boot by cmd/skygate/main.go and registered as the handler
// for GET /healthz and GET /readyz. All fields are read-only
// after construction; no internal state beyond the process-wide
// readyz cache (which is fine because the cache is keyed by
// wall-clock, not by Service identity).
//
// Field semantics:
//   - DB:        the open *sql.DB. Required (liveness will fail
//                otherwise — see /readyz).
//   - HeadscaleFn: a function that returns the current headscale
//                client (nil if disabled). Modeled as a function
//                (not a *headscale.Client) so the Service
//                re-reads the active client on every probe —
//                a v0.12.0+ per-user headscale swap doesn't
//                leave the readiness probe stuck on a stale
//                client. Passed as a method value (e.g.
//                `app.HSGlobal`).
//   - InstanceID: the SKYGATE_INSTANCE_ID env value, or
//                "unconfigured".
//   - BuildVersion: "vX.Y.Z+<commit>" set by main.go at boot.
//   - StartedAt:  wall-clock when main() returned from setup.
type Service struct {
	DB           *sql.DB
	HeadscaleFn  func() headscale.Pingable
	InstanceID   string
	BuildVersion string
	StartedAt    time.Time
}

// GetHealthz — liveness probe. Always 200 OK with a
// tiny JSON body. Never touches the DB or headscale
// (that's what /readyz is for). K8s livenessProbe.
func (s *Service) GetHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"instance_id": s.InstanceID,
		"build":       s.BuildVersion,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
}

// GetReadyz — readiness probe. Pings the DB and the
// headscale client (if configured), caches the result
// for 1 second, returns 200 OK or 503 Service Unavailable
// with a per-component breakdown. K8s readinessProbe.
func (s *Service) GetReadyz(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	last := readyz.lastAt.Load()
	if now-last < readyzCacheTTL {
		// Fresh cache — return it.
		if cached := readyz.state.Load(); cached != nil {
			s.writeReadyz(w, cached)
			return
		}
	}
	// Cache miss (or first call). Run the checks.
	state := s.runReadyzChecks(r.Context())
	readyz.state.Store(&state)
	readyz.lastAt.Store(now)
	s.writeReadyz(w, &state)
}

// runReadyzChecks does the actual readiness checks. Each
// check is independently wrapped so a slow DB doesn't
// block the headscale check (and vice versa). The
// overall Healthy flag is AND-of-all-checks.
func (s *Service) runReadyzChecks(ctx context.Context) readyzState {
	state := readyzState{
		Checks:     map[string]string{},
		InstanceID: s.InstanceID,
		Build:      s.BuildVersion,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		UptimeSec:  int64(time.Since(s.StartedAt).Seconds()),
	}
	// DB check — cheap (a single ping).
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(dbCtx); err != nil {
		state.DB = "error: " + err.Error()
		state.Checks["db"] = "fail"
	} else {
		state.DB = "ok"
		state.Checks["db"] = "ok"
	}
	// Headscale check — only if a client is configured.
	// Read-only deploys (HeadscaleFn returns nil) get
	// "skipped" so the operator can tell at a glance.
	hs := s.HeadscaleFn()
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

func (s *Service) writeReadyz(w http.ResponseWriter, state *readyzState) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !state.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(state)
}
