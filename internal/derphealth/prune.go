package derphealth

// prune.go — B317: a region that is no longer in the probe list must not keep a
// verdict on the dashboard.
//
// WHY. `derp_health` is a cache of the LAST probe per region, and the dashboard
// renders the table as-is. B317 stopped probing rows whose URL is not a relay, so
// the legacy region-901 row (Tailscale's control plane, migrated from
// `derp.external_urls`) is no longer refreshed — but its last verdict would sit in
// the table for ever, reading "public, healthy, 100 ms" on a page whose job is to
// answer "which DERP can this server use". The same is true of any relay the
// operator removes or renames: the row is evidence of a relay that no longer exists
// in the map.
//
// The prune is deliberately narrow: it only runs where the FULL list is known (the
// cron tick and the dashboard's "Re-probe all"), never from `ProbeAll` itself and
// never from a filtered CLI run — `skygate derp-probe -own-only` probes two rows and
// must not conclude that the other twenty-eight are gone.

import (
	"context"
	"database/sql"
	"log"

	"skygate/internal/db"
)

// PruneMissingRegions deletes derp_health rows whose region_id is absent from
// `derps`, and returns how many rows it removed.
//
// An EMPTY or nil `derps` is a no-op on purpose: that is the shape of "the public
// map fetch failed and the DB had no rows either", and turning a failed fetch into
// an empty dashboard would be worse than a stale row.
func PruneMissingRegions(ctx context.Context, d *sql.DB, derps []DERPInfo) (int, error) {
	if d == nil || len(derps) == 0 {
		return 0, nil
	}
	keep := make([]any, 0, len(derps))
	seen := make(map[int]bool, len(derps))
	for _, info := range derps {
		if seen[info.RegionID] {
			continue
		}
		seen[info.RegionID] = true
		keep = append(keep, info.RegionID)
	}
	q := "DELETE FROM derp_health WHERE region_id NOT IN (" + db.PlaceholdersList(len(keep)) + ")"
	res, err := d.ExecContext(ctx, q, keep...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("derphealth: pruned %d derp_health row(s) for regions no longer in the map", n)
	}
	return int(n), nil
}
