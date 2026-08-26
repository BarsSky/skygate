package derphealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PublicMapURL is the canonical Tailscale default DERP map.
// Refreshed every 24h by headscale per its config.yaml
// (derp.update_frequency). 30s timeout is generous; the
// file is ~15KB and Tailscale's CDN is fast.
const PublicMapURL = "https://controlplane.tailscale.com/derpmap/default"

// mapFetchTimeout bounds the time we wait for the Tailscale
// map fetch. If the fetch fails, we still probe whatever
// own derp_relays we have in skygate's DB — partial data
// is better than nothing.
const mapFetchTimeout = 30 * time.Second

// derpMapResponse mirrors the structure of Tailscale's
// derpmap/default JSON. The fields we use:
//   - Regions[id].Nodes[0].Name  : canonical DERP hostname
//   - Regions[id].Nodes[0].RegionCode : IATA-ish 3-letter code
//   - Regions[id].Nodes[0].Locality : human-readable city
//   - Regions[id].Nodes[0].Country  : 2-letter ISO code
// Other fields (lat/long, capabilities, etc.) are read-
// only here; we don't preserve them to keep the struct
// minimal. Add as needed.
type derpMapResponse struct {
	Regions map[string]struct {
		Nodes []struct {
			Name       string `json:"Name"`
			RegionCode string `json:"RegionCode"`
			Locality   string `json:"Locality,omitempty"`
			Country    string `json:"Country,omitempty"`
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
		out = append(out, DERPInfo{
			RegionID:   id,
			RegionCode: n.RegionCode,
			RegionName: n.RegionCode, // map doesn't include a long name
			Locality:   n.Locality,
			Country:    n.Country,
			Host:       n.Name,
			URL:        "https://" + n.Name,
			IsOwn:      false,
		})
	}
	return out, nil
}

// FetchOwnDERPs reads the operator's own derp_relays table
// and returns one DERPInfo per enabled row. region_id 901
// is the bundled Tailscale default (always returned if
// enabled) — we keep it marked IsOwn=false because it
// doesn't actually live on the operator's infrastructure
// (it's the Tailscale control plane's default relay).
//
// Caller is responsible for passing an open *sql.DB. We
// don't open the connection here so the function can be
// unit-tested with a tx.
func FetchOwnDERPs(ctx context.Context, db *sql.DB) ([]DERPInfo, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT region_id, COALESCE(region_code, ''),
		       COALESCE(region_name, ''), COALESCE(hostname, ''),
		       COALESCE(url, ''), is_bundled
		  FROM derp_relays
		 WHERE enabled = 1
		 ORDER BY is_bundled ASC, sort_order ASC, region_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("own derp_relays query: %w", err)
	}
	defer rows.Close()
	var out []DERPInfo
	for rows.Next() {
		var d DERPInfo
		var isBundled int
		if err := rows.Scan(&d.RegionID, &d.RegionCode, &d.RegionName,
			&d.Host, &d.URL, &isBundled); err != nil {
			return nil, fmt.Errorf("own derp_relays scan: %w", err)
		}
		// Bundled Tailscale DERP is "is_own" in derp_relays
		// but it doesn't actually live on the operator's
		// infra. Mark it IsOwn=false so the dashboard
		// shows it as a public-like entry (with a small
		// "bundled" badge in the UI).
		d.IsOwn = isBundled == 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// FetchAllDERPs returns the union of FetchPublicDERPs +
// FetchOwnDERPs. Duplicates by RegionID are deduplicated
// (the own DERP wins over the public, since the public
// DERP map is read-only and the operator's own entries
// reflect their explicit choice).
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
	// Dedup by RegionID: own wins.
	byID := make(map[int]DERPInfo, len(own)+len(public))
	for _, d := range own {
		byID[d.RegionID] = d
	}
	for _, d := range public {
		if _, exists := byID[d.RegionID]; !exists {
			byID[d.RegionID] = d
		}
	}
	out := make([]DERPInfo, 0, len(byID))
	for _, d := range byID {
		out = append(out, d)
	}
	return out, nil
}
