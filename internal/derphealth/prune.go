package derphealth

// prune.go — B317: a region that is no longer in the probe list must not keep a
// verdict on the dashboard.
//
// WHY. `derp_health` is a cache of the LAST probe per relay, and the dashboard
// renders the table as-is. B317 stopped probing rows whose URL is not a relay, so
// the legacy region-901 row (Tailscale's control plane, migrated from
// `derp.external_urls`) is no longer refreshed — but its last verdict would sit in
// the table for ever, reading "public, healthy, 100 ms" on a page whose job is to
// answer "which DERP can this server use". The same is true of any relay the
// operator removes or renames: the row is evidence of a relay that no longer exists
// in the map.
//
// B457 (2026-10-31): the prune identifies a relay by (region_id, URL), matching the key
// the table now uses — the live pair differed by PORT on one hostname, so the URL is
// what tells two relays of one region apart. Keying it by region alone would delete a
// relay the prune simply did not see: the moment a region holds a stale second row and
// the map lists a different one, the region-only delete took BOTH and the dashboard
// lost a relay it had measured.
//
// The prune is deliberately narrow: it only runs where the FULL list is known (the
// cron tick and the dashboard's "Re-probe all"), never from `ProbeAll` itself and
// never from a filtered CLI run — `skygate derp-probe -own-only` probes two rows and
// must not conclude that the other twenty-eight are gone.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// PruneMissingRelays deletes derp_health rows whose (region_id, url) pair is absent
// from `derps`, and returns how many rows it removed.
//
// An EMPTY or nil `derps` is a no-op on purpose: that is the shape of "the public
// map fetch failed and the DB had no rows either", and turning a failed fetch into
// an empty dashboard would be worse than a stale row.
func PruneMissingRelays(ctx context.Context, d *sql.DB, derps []DERPInfo) (int, error) {
	if d == nil || len(derps) == 0 {
		return 0, nil
	}
	// Keep-list as OR-ed (region_id, url) clauses: one statement, so a row cannot be
	// deleted by a partially-applied list. The URL compare is case-insensitive because
	// a hostname in the derpmap and one typed by the operator can differ in case, and a
	// prune must never delete a relay over a capital letter.
	clauses := make([]string, 0, len(derps))
	args := make([]any, 0, len(derps)*2)
	seen := make(map[string]bool, len(derps))
	for _, info := range derps {
		if strings.TrimSpace(info.URL) == "" {
			continue
		}
		key := fmt.Sprintf("%d|%s", info.RegionID, strings.ToLower(info.URL))
		if seen[key] {
			continue
		}
		seen[key] = true
		clauses = append(clauses,
			fmt.Sprintf("(region_id = $%d AND lower(url) = lower($%d))", len(args)+1, len(args)+2))
		args = append(args, info.RegionID, info.URL)
	}
	if len(clauses) == 0 {
		return 0, nil
	}
	q := "DELETE FROM derp_health WHERE NOT (" + strings.Join(clauses, " OR ") + ")"
	res, err := d.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("derphealth: pruned %d derp_health row(s) for relays no longer in the map", n)
	}
	return int(n), nil
}
