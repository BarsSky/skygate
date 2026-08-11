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
//   - Availability: optional v0.33.1.40 B92 background checker.
//                When non-nil, the headscale/headplane/tailscale
//                fields in /readyz are read from the CACHED
//                snapshot (refreshed every 30s by the checker)
//                instead of synchronously probing headscale
//                on every scrape. The headscale field is still
//                used as the per-component summary; the full
//                snapshot is included under `availability` for
//                callers that want latency / last_checked /
//                error details.
//   - InstanceID: the SKYGATE_INSTANCE_ID env value, or
//                "unconfigured".
//   - BuildVersion: "vX.Y.Z+<commit>" set by main.go at boot.
//   - StartedAt:  wall-clock when main() returned from setup.
type Service struct {
	DB           *sql.DB
	HeadscaleFn  func() headscale.Pingable
	Availability *Checker
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
// overall Healthy flag is DB-only (see D5 below).
//
// v0.33.1.40 B92: when the Availability Checker is wired
// (the default for v0.33.1.40+), the headscale/headplane/
// tailscale fields are populated from the CACHED snapshot
// (refreshed every 30s by the background goroutine). The
// live HeadscaleFn is used as a one-shot fallback for
// installations that haven't enabled the checker yet.
//
// v0.33.1.42 D5: /readyz semantics change. Pre-D5, the
// healthy flag was AND-of-all (DB + headscale + headplane +
// tailscale). A headscale outage flipped the whole instance
// to "unhealthy" (503) even though the DB was fine and skygate
// could still serve /my/devices, /my/exit-rules, /admin/headscale
// config, etc. — those endpoints just needed to talk to
// headscale, which they do non-blockingly. Post-D5, the
// healthy flag is DB-only: skygate is "ready" iff the DB
// is reachable. The per-integration statuses (headscale,
// headplane, tailscale) are still populated and shown in
// the JSON, but they're ADVISORY — they don't gate the
// 200/503 status code.
//
// Rationale: the B91 architectural principle is "skygate
// starts independently of headscale". /readyz should
// reflect that. If the operator wants a stricter readiness
// probe (e.g. K8s readinessProbe that gates traffic on
// headscale being up), they can chain: check /readyz's
// `headscale` field via a custom K8s readinessProbe
// httpGet (or use a /readyz-strict endpoint — future work).
//
// The /readyz JSON shape gains a new top-level
// `DependenciesHealthy` field (the AND-of-all pre-D5
// behavior, kept for tools that want the stricter view).
// The top-level `Healthy` field is now DB-only.
func (s *Service) runReadyzChecks(ctx context.Context) readyzState {
	state := readyzState{
		Checks:     map[string]string{},
		InstanceID: s.InstanceID,
		Build:      s.BuildVersion,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		UptimeSec:  int64(time.Since(s.StartedAt).Seconds()),
	}
	// DB check — cheap (a single ping). The ONLY gate for
	// the top-level `Healthy` field. /readyz returns 200
	// iff DB is reachable; otherwise 503.
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(dbCtx); err != nil {
		state.DB = "error: " + err.Error()
		state.Checks["db"] = "fail"
	} else {
		state.DB = "ok"
		state.Checks["db"] = "ok"
	}
	// Headscale + Headplane + Tailscale — prefer cached
	// availability (B92) so /readyz returns in <5ms even
	// when headscale is slow or down. Fall back to live
	// HeadscaleFn if the checker isn't wired (v0.33.1.39
	// and earlier behavior, kept for safety).
	if s.Availability != nil {
		s.populateFromAvailability(&state)
	} else {
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
		state.Headplane = "skipped (no availability checker wired)"
		state.Tailscale = "skipped (no availability checker wired)"
		state.Checks["headplane"] = "skipped"
		state.Checks["tailscale"] = "skipped"
	}
	// D5: top-level Healthy is DB-only. DependenciesHealthy
	// keeps the pre-D5 AND-of-all behavior for tools that
	// want the stricter view.
	state.Healthy = state.DB == "ok"
	state.DependenciesHealthy = state.Healthy &&
		isOKOrSkipped(state.Headscale) &&
		isOKOrSkipped(state.Headplane) &&
		isOKOrSkipped(state.Tailscale)
	return state
}

// isOKOrSkipped treats "ok" and any "skipped*" string as
// healthy (so the readiness probe doesn't go red just because
// the operator hasn't configured headplane or Tailscale).
func isOKOrSkipped(s string) bool {
	if s == "ok" {
		return true
	}
	if len(s) >= 7 && s[:7] == "skipped" {
		return true
	}
	return false
}

// populateFromAvailability reads the cached snapshot and
// fills the per-component fields. The snapshot is updated
// every 30s by the background Checker, so the values here
// are at most 30s stale.
func (s *Service) populateFromAvailability(state *readyzState) {
	avail := s.Availability.Snapshot()
	state.Availability = avail
	for _, integ := range avail.Integrations {
		summary, checkStatus := summaryForIntegration(integ)
		switch integ.ID {
		case IntegrationHeadscale:
			state.Headscale = summary
		case IntegrationHeadplane:
			state.Headplane = summary
		case IntegrationTailscale:
			state.Tailscale = summary
		}
		state.Checks[string(integ.ID)] = checkStatus
	}
}

// summaryForIntegration converts the per-integration struct
// into the short string used in /readyz (matches the pre-
// v0.33.1.40 vocabulary: "ok" / "error: <reason>" / "skipped
// (no headscale configured)" / "checking...").
func summaryForIntegration(integ IntegrationStatus) (summary, checkStatus string) {
	if integ.IsZero() {
		switch integ.ID {
		case IntegrationHeadscale:
			return "skipped (no headscale configured)", "skipped"
		case IntegrationHeadplane:
			return "skipped (no headplane configured)", "skipped"
		case IntegrationTailscale:
			return "skipped (tailscale disabled)", "skipped"
		}
		return "skipped (not configured)", "skipped"
	}
	if !integ.OK {
		if integ.Error != "" {
			return "error: " + integ.Error, "fail"
		}
		return "down", "fail"
	}
	return "ok", "ok"
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
