package derphealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"skygate/internal/derpcfg"
)

// PublicMapURL is the canonical Tailscale default DERP map.
// Refreshed every 24h by headscale per its config.yaml
// (derp.update_frequency). 30s timeout is generous; the
// file is ~15KB and Tailscale's CDN is fast.
//
// B235: this is a `var` (not `const`) so unit tests can
// override it with httptest.NewServer. Production code
// never writes to it. The pre-B235 test in
// derphealth_test.go only validated JSON unmarshal;
// the B235 tests in map_b235_test.go need a real HTTP
// roundtrip to verify the full path, which requires
// the URL to be swappable.
var PublicMapURL = "https://controlplane.tailscale.com/derpmap/default"

// mapFetchTimeout bounds the time we wait for the Tailscale
// map fetch. If the fetch fails, we still probe whatever
// own derp_relays we have in skygate's DB — partial data
// is better than nothing.
const mapFetchTimeout = 30 * time.Second

// derpMapResponse mirrors the structure of Tailscale's
// derpmap/default JSON. The fields we use:
//   - Regions[id].RegionCode   : IATA-ish 3-letter code
//     ("nyc", "waw", "sin"). Tailscale puts this on the
//     REGION level, not the node — pre-B235.1 my struct
//     had it on the node, so the field was always empty
//     and the main-page "Active DERP" hero showed
//     "ms" without a region code.
//   - Regions[id].RegionName   : human-readable city
//     ("New York City"). Same level.
//   - Regions[id].Nodes[0].HostName  : FQDN
//     ("derp1f.tailscale.com") — used as the probe host
//     and as the URL host. THIS is what the TLS
//     handshake dials.
//   - Regions[id].Nodes[0].Name     : short label ("1f").
//     Kept for display ("DERP 1f" vs "DERP
//     derp1f.tailscale.com") but NOT for network
//     operations — B235 fix; pre-B235 the code used
//     `n.Name` for Host, which is the SHORT label and
//     not a resolvable DNS name, so every public DERP
//     probe failed with "no such host" and the
//     dashboard showed 28/28 as "degraded".
//   - Regions[id].Nodes[0].Locality + Country : node-
//     level metadata, same shape as the region-level
//     fields (Tailscale keeps them on both, for
//     backwards compat).
//
// Other fields (lat/long, capabilities, etc.) are
// not preserved to keep the struct minimal.
type derpMapResponse struct {
	Regions map[string]struct {
		RegionID   int    `json:"RegionID"`
		RegionCode string `json:"RegionCode"`
		RegionName string `json:"RegionName"`
		Nodes      []struct {
			Name     string `json:"Name"`
			HostName string `json:"HostName"`
			Locality string `json:"Locality,omitempty"`
			Country  string `json:"Country,omitempty"`
		} `json:"Nodes"`
	} `json:"Regions"`
}

// FetchPublicDERPs parses Tailscale's default DERP map and
// returns one DERPInfo per region. The first node of each
// region is used as the canonical host/region_code/etc.
// (Tailscale regions have 3-5 nodes for redundancy; the
// others can be discovered via the same endpoint if
// needed; for the dashboard we only need the canonical
// representative per region).
//
// Returns an error if the HTTP fetch or JSON parse fails;
// the caller should still probe the own DERP list in that
// case (partial data > no data).
func FetchPublicDERPs(ctx context.Context, httpClient *http.Client) ([]DERPInfo, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: mapFetchTimeout}
	}
	cctx, cancel := context.WithTimeout(ctx, mapFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, "GET", PublicMapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("derp map request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("derp map fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("derp map http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("derp map read: %w", err)
	}
	var raw derpMapResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("derp map parse: %w", err)
	}
	out := make([]DERPInfo, 0, len(raw.Regions))
	for idStr, r := range raw.Regions {
		if len(r.Nodes) == 0 {
			continue
		}
		n := r.Nodes[0]
		var id int
		if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
			continue
		}
		// B235: the pre-fix code used `n.Name` ("1f", "22w")
		// as Host. That's a Tailscale-internal short label
		// — NOT a resolvable DNS name. Probes to
		// "1f:443" failed with "no such host" and
		// marked every public DERP as degraded. Use
		// `n.HostName` (the FQDN like "derp1f.tailscale.com")
		// for the network-touching fields. The short
		// `n.Name` is preserved as `Name` (separate
		// field, used by the dashboard's display label).
		host := n.HostName
		if host == "" {
			// Fallback: the public derpmap response should
			// always include HostName. If a future Tailscale
			// API change drops it, fall back to Name so
			// we at least get a resolvable A-record (some
			// clients may serve DERP on the short label
			// too, but we don't depend on that).
			host = n.Name
		}
		out = append(out, DERPInfo{
			RegionID:   id,
			RegionCode: r.RegionCode,
			RegionName: r.RegionName,
			Locality:   n.Locality,
			Country:    n.Country,
			Host:       host,
			Name:       n.Name, // short label for display
			URL:        "https://" + host,
			IsOwn:      false,
		})
	}
	return out, nil
}

// FetchOwnDERPs reads the operator's own derp_relays table
// and returns one DERPInfo per enabled row.
//
// B265 (2026-09-19) — IsOwn semantics fixed. Pre-B265 this was
//
//	d.IsOwn = isBundled == 0
//
// which is INVERTED: `is_bundled = 1` marks the operator's OWN
// locally-hosted derper (see internal/feature/admin/derp_relays_auto.go
// — EnsureBundledDerpRelay inserts the local derper with
// is_bundled=1, region 900), while is_bundled=0 marks an EXTERNAL
// relay an operator typed in (typically the Tailscale public derpmap
// URL migrated by AutoMigrateDerpRelays into region 901).
//
// Live symptom of the inversion (reference VM 2026-09-19):
//
//	derp_health: region 901 (controlplane.tailscale.com) is_own=1
//	             region 900 (derp.skynas.ru, the local derper) is_own=0
//
// so `derp_dashboard.go`'s "Recommended DERP" banner and
// `my/dashboard.go`'s bestHealthyDERP both selected region 901
// (108 ms, a relay that does not exist at that hostname) while the
// operator's own relay measured 12 ms. The owner badge on the
// dashboard also read "public" for the local relay and "own" for
// the public one.
func FetchOwnDERPs(ctx context.Context, db *sql.DB) ([]DERPInfo, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT region_id, COALESCE(region_code, ''),
		       COALESCE(region_name, ''), COALESCE(hostname, ''),
		       COALESCE(url, ''), is_bundled
		  FROM derp_relays
		 WHERE enabled = 1
		 ORDER BY is_bundled DESC, sort_order ASC, region_id ASC, url ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("own derp_relays query: %w", err)
	}
	defer rows.Close()
	// B296: resolve the probe hint ONCE for the whole list — an operator who
	// typed an address on /admin/derp/relays gets it applied to the next cron
	// tick with no container recreate (env_file is fixed at creation).
	probeHost := derpcfg.DialHost(db)
	var out []DERPInfo
	for rows.Next() {
		var d DERPInfo
		var isBundled int
		if err := rows.Scan(&d.RegionID, &d.RegionCode, &d.RegionName,
			&d.Host, &d.URL, &isBundled); err != nil {
			return nil, fmt.Errorf("own derp_relays scan: %w", err)
		}
		// B317: a row whose URL is a derpmap DOCUMENT is not a relay and must not
		// be probed as one. The legacy `derp.external_urls` value was migrated into
		// derp_relays as region 901 with the URL of Tailscale's public map, and the
		// prober happily dialled controlplane.tailscale.com:443 — Tailscale's
		// CONTROL PLANE — then filed the result as "region 901, public, healthy,
		// 117 ms" and let the dashboard RECOMMEND it over the operator's own relay
		// 12 ms away. The public map has no region 901 (verified live: 28 regions,
		// none of them 901), so nothing legitimate is lost by skipping it; the
		// /admin/derp/relays page already names this row's reason, and the journal
		// gets a line here.
		if !urlIsRelayNode(d.URL) {
			log.Printf("derphealth: skipping region %d row %q — its URL is not a relay endpoint (%s); see /admin/derp/relays",
				d.RegionID, d.Host, d.URL)
			continue
		}
		// B265: a bundled row IS the operator's own infrastructure
		// (the local derper). An external row (is_bundled=0) is NOT
		// — it points at somebody else's relay.
		d.IsOwn = derpRelayIsOwn(isBundled)
		d.ProbeHost = probeHost
		out = append(out, d)
	}
	return out, rows.Err()
}

// urlIsRelayNode reports whether a derp_relays URL names a single relay ENDPOINT
// (as opposed to a derpmap document or any other non-relay path).
//
// B317 — this mirrors internal/feature/admin.isDerpMapURL (B265), which already
// keeps such rows out of the map skygate PUBLISHES. The publisher and the health
// prober are the two halves of the same claim ("this row is a relay"), and they
// disagreed: the map skipped the region-901 derpmap row while the prober measured
// it and the dashboard recommended it. Keep the two rules in step.
func urlIsRelayNode(rawURL string) bool {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	// A hostname with no path is a relay (the protocol lives on / and /derp).
	switch strings.TrimRight(u.Path, "/") {
	case "", "/derp", "/derp/probe", "/derp/latency-check":
		return true
	}
	// Everything else — /derpmap/default above all — is a document, not a relay.
	return false
}

// derpRelayIsOwn maps the derp_relays.is_bundled flag to the
// "is this the operator's own relay?" answer used by the dashboard
// and the main-page hero.
//
// B265 (2026-09-19) — extracted as a pure function because the
// inline expression was INVERTED (`isBundled == 0`) and nothing
// tested it:
//
//	is_bundled = 1 → the operator's own locally-hosted derper
//	                 (internal/feature/admin/derp_relays_auto.go
//	                 EnsureBundledDerpRelay inserts it for region
//	                 900; the operator's /admin/derp/relays rows
//	                 for their own relays are bundled too)
//	is_bundled = 0 → an EXTERNAL relay row (e.g. the region-901
//	                 row AutoMigrateDerpRelays created from the
//	                 legacy derp.external_urls)
//
// Consequence of the old inversion on the reference VM: the
// dashboard recommended region 901 (controlplane.tailscale.com,
// a derpmap URL, not a relay) over the operator's own derper, and
// the main-page "Active DERP" hero showed 108 ms instead of the
// local 12 ms.
func derpRelayIsOwn(isBundled int) bool {
	return isBundled == 1
}

// FetchAllDERPs returns the union of FetchPublicDERPs + FetchOwnDERPs.
//
// B317 — TWO rules, both learned from the live "the local relay is never the
// recommended one" report:
//
//  1. EVERY enabled own row is kept, even when several share a region_id. The
//     pre-B317 dedup collapsed them in a `map[int]DERPInfo` loop, so for region
//     900 the LAST row in query order won — and the operator's rows are
//     `…:443` (sort_order 10) and a stale `…:8443` (sort_order 11) — the DEAD one.
//     The stale row then owned region 900's single derp_health row and reported
//     `dial tcp …:8443: connect: connection refused` forever, which is why the
//     dashboard hid the local relay and recommended a public one 100 ms away.
//     Collapsing is the PROBER's job now, and it does it by MEASURING every row
//     and keeping the best (see BestPerRegion).
//  2. The result order is deterministic (own first, then region_id, then URL)
//     instead of the map-iteration order, so two runs cannot disagree about
//     which row is "first" — and a caller that persists per row can never flip
//     a region's health by luck.
//
// Public rows still lose to an own row for the same region: the public map is
// read-only and the operator's row is an explicit choice.
func FetchAllDERPs(ctx context.Context, db *sql.DB, httpClient *http.Client) ([]DERPInfo, error) {
	own, err := FetchOwnDERPs(ctx, db)
	if err != nil {
		// Don't fail the whole call on a DB hiccup;
		// the dashboard can still show the public map.
		own = nil
	}
	public, err := FetchPublicDERPs(ctx, httpClient)
	if err != nil {
		public = nil
	}
	ownRegions := make(map[int]bool, len(own))
	out := make([]DERPInfo, 0, len(own)+len(public))
	for _, d := range own {
		out = append(out, d)
		ownRegions[d.RegionID] = true
	}
	for _, d := range public {
		if !ownRegions[d.RegionID] {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsOwn != out[j].IsOwn {
			return out[i].IsOwn
		}
		if out[i].RegionID != out[j].RegionID {
			return out[i].RegionID < out[j].RegionID
		}
		return out[i].URL < out[j].URL
	})
	return out, nil
}
