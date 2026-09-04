// Package derphealth (B189) — DERP server health dashboard.
//
// Caches live latency measurements for every known DERP
// server (Tailscale's 28 public regions + the operator's
// own derp_relays rows). The probe is a TLS-handshake
// measurement against the derper's :443 endpoint; we don't
// trust ICMP because many cloud DERP servers are configured
// to not respond to ping.
//
// The data is persisted in the derp_health table (see
// migrateV062PG). The dashboard at /admin/derp/dashboard
// reads from this table.
package derphealth

import (
	"context"
	"time"
)

// DERPInfo describes a single DERP server's static metadata.
// Latency / health is in HealthRow; metadata is set once
// when the row is first discovered and rarely changes.
type DERPInfo struct {
	RegionID   int    `json:"region_id"`
	RegionCode string `json:"region_code"`
	RegionName string `json:"region_name"`
	Locality   string `json:"locality"`
	Country    string `json:"country"`
	// Host is the FQDN used for the probe (e.g.
	// "derp1f.tailscale.com"). B235 fix: pre-B235
	// this was the SHORT label ("1f", "22w") — not
	// a resolvable DNS name — so every public DERP
	// probe failed with "no such host". See
	// map.go FetchPublicDERPs for the parse step.
	Host string `json:"host"`
	// Name is the SHORT label ("1f", "22w") used for
	// dashboard display. Distinct from Host since
	// the dashboard shows both (e.g. "derp1f (1f) WAW").
	// Empty for own DERP rows (the operator configures
	// the hostname but not a short label).
	Name string `json:"name,omitempty"`
	URL   string `json:"url"`
	IsOwn bool   `json:"is_own"`
}

// HealthRow is the live state of a DERP as cached in
// derp_health. The dashboard renders one row per DERP.
type HealthRow struct {
	DERPInfo
	LatencyMs    int       `json:"latency_ms"`
	LastCheck    time.Time `json:"last_check"`
	Healthy      bool      `json:"healthy"`
	LastError    string    `json:"last_error"`
	ProbesTotal  int       `json:"probes_total"`
	ProbesFailed int       `json:"probes_failed"`
}

// probeTimeout is the per-DERP TLS-handshake deadline. 3s
// is enough for the slowest realistic DERP (Tailscale's
// Singapore from Europe); anything beyond that is a dead
// DERP from the operator's perspective.
const probeTimeout = 3 * time.Second

// UpsertQuery inserts/updates one row in derp_health. The
// ON CONFLICT (region_id) DO UPDATE means re-probing just
// refreshes the latency / health / counter columns; the
// metadata (host, region_code, region_name, name, etc.)
// is left alone once the row exists.
//
// B235.3: now writes the `name` column (Tailscale short
// label like "1f", "22w") for public DERP rows. The
// /admin/derp/dashboard template renders this as a
// `.Name=` pill next to the FQDN host cell.
const upsertQuery = `
INSERT INTO derp_health
  (region_id, is_own, host, url, name, region_code, region_name,
   locality, country, latency_ms, last_check, healthy,
   last_error, probes_total, probes_failed)
VALUES
  ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (region_id) DO UPDATE SET
  is_own        = EXCLUDED.is_own,
  host          = EXCLUDED.host,
  url           = EXCLUDED.url,
  name          = EXCLUDED.name,
  region_code   = EXCLUDED.region_code,
  region_name   = EXCLUDED.region_name,
  locality      = EXCLUDED.locality,
  country       = EXCLUDED.country,
  latency_ms    = EXCLUDED.latency_ms,
  last_check    = EXCLUDED.last_check,
  healthy       = EXCLUDED.healthy,
  last_error    = EXCLUDED.last_error,
  probes_total  = derp_health.probes_total + 1,
  probes_failed = CASE
                    WHEN EXCLUDED.healthy = 0
                    THEN derp_health.probes_failed + 1
                    ELSE 0
                  END
`

// _ prevents unused-import warning when the file is
// the only one in the package. Remove as more files are
// added.
var _ = context.Background
