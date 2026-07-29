// Package healthz is the feature module for the liveness/readiness
// probes. See doc.go for the full design.
//
// types.go — the response struct returned by GET /readyz and the
// per-component check result type. Kept separate from service.go so
// the JSON shape is easy to find / change / lint independently of
// the handler logic.
package healthz

// readyzState is the body returned by GET /readyz.
// The HTTP status is 200 if Healthy=true, 503 otherwise.
//
// JSON shape (stable contract for external monitors like
// Prometheus blackbox-exporter, Grafana, opsgenie, etc.):
//
//	{
//	  "healthy":     true,
//	  "db":          "ok",
//	  "headscale":   "ok" | "skipped (no headscale configured)" | "error: <reason>",
//	  "instance_id": "<SKYGATE_INSTANCE_ID or unconfigured>",
//	  "build":       "v0.31.0+<commit>",
//	  "uptime_sec":  12345,
//	  "timestamp":   "2026-07-29T10:11:12Z",
//	  "checks":      {"db": "ok", "headscale": "ok"}
//	}
type readyzState struct {
	Healthy    bool              `json:"healthy"`
	DB         string            `json:"db"`          // "ok" / "error: <reason>"
	Headscale  string            `json:"headscale"`   // "ok" / "skipped" / "error: <reason>"
	InstanceID string            `json:"instance_id"` // SKYGATE_INSTANCE_ID env, or "unconfigured"
	Build      string            `json:"build"`       // vX.Y.Z + commit (set at boot)
	UptimeSec  int64             `json:"uptime_sec"`
	Timestamp  string            `json:"timestamp"` // RFC3339, server time
	Checks     map[string]string `json:"checks"`    // individual check results
}
