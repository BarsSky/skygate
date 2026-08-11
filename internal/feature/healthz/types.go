// Package healthz is the feature module for the liveness/readiness
// probes. See doc.go for the full design.
//
// types.go — the response struct returned by GET /readyz and the
// per-component check result type. Kept separate from service.go so
// the JSON shape is easy to find / change / lint independently of
// the handler logic.
package healthz

// readyzState is the body returned by GET /readyz.
// The HTTP status is 200 if Healthy=true, http.StatusServiceUnavailable otherwise.
//
// JSON shape (stable contract for external monitors like
// Prometheus blackbox-exporter, Grafana, opsgenie, etc.):
//
//	{
//	  "healthy":     true,
//	  "db":          "ok",
//	  "headscale":   "ok" | "skipped (no headscale configured)" | "error: <reason>",
//	  "headplane":   "ok" | "skipped" | "checking" | "error: <reason>",
//	  "tailscale":   "ok" | "skipped" | "checking" | "error: <reason>",
//	  "instance_id": "<SKYGATE_INSTANCE_ID or unconfigured>",
//	  "build":       "v0.31.0+<commit>",
//	  "uptime_sec":  12345,
//	  "timestamp":   "2026-07-29T10:11:12Z",
//	  "checks":      {"db": "ok", "headscale": "ok", "headplane": "ok", "tailscale": "ok"},
//	  "availability": {<full Availability struct, see availability.go>}
//	}
//
// v0.33.1.40 B92: added `headplane` and `tailscale` fields
// backed by the periodic Availability Checker. The Checker
// runs every 30s in a background goroutine and caches the
// latest result; /readyz returns the CACHED status (no live
// headscale probe), so a headscale outage doesn't slow down
// the readiness response. The full Availability snapshot is
// included as `availability` for callers that want latency,
// last_checked, and error details.
//
// v0.33.1.42 D5: split `healthy` into two fields:
//   - `healthy` (top-level): DB-only. /readyz returns 200 iff
//     this is true. Reflects the B91 architectural principle
//     that skygate starts independently of headscale — the
//     readiness probe shouldn't go red just because headscale
//     is briefly unreachable.
//   - `dependencies_healthy`: pre-D5 AND-of-all (DB +
//     headscale + headplane + tailscale). For tools that
//     want the stricter view (e.g. K8s readinessProbe that
//     gates traffic on every dependency being up).
type readyzState struct {
	Healthy            bool              `json:"healthy"`             // DB-only (D5)
	DependenciesHealthy bool              `json:"dependencies_healthy"` // AND-of-all (D5, pre-D5 behavior)
	DB                 string            `json:"db"`                   // "ok" / "error: <reason>"
	Headscale          string            `json:"headscale"`            // "ok" / "skipped" / "error: <reason>"
	Headplane          string            `json:"headplane"`            // "ok" / "skipped" / "checking" / "error: <reason>"
	Tailscale          string            `json:"tailscale"`            // "ok" / "skipped" / "checking" / "error: <reason>"
	InstanceID         string            `json:"instance_id"`          // SKYGATE_INSTANCE_ID env, or "unconfigured"
	Build              string            `json:"build"`                // vX.Y.Z + commit (set at boot)
	UptimeSec          int64             `json:"uptime_sec"`
	Timestamp          string            `json:"timestamp"`            // RFC3339, server time
	Checks             map[string]string `json:"checks"`               // individual check results
	Availability       *Availability     `json:"availability,omitempty"` // B92: full snapshot
}
