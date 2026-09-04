// File: internal/feature/admin/derp_dashboard.go
// B189 (v1.5.2) — DERP Health Dashboard.
// B228 (v1.5.2+) — "hide unavailable" filter (operator
//   2026-09-03: 28+ rows of "degraded, —" на странице
//   делают dashboard бесполезным — нужно сразу отбросить
//   недоступные и оставить только healthy, отсортированные
//   по latency. По умолчанию фильтр ВКЛ (соответствует
//   предпочтению operator'а), тогглер в UI позволяет
//   включить "show all" для отладки).
//
// Renders /admin/derp/dashboard with one row per known
// DERP server (own + Tailscale's 28 public regions) showing
// the most recent probe's latency, health, and error.
// Probing happens in the background (derphealth.StartCron
// in cmd/skygate/main.go, 5-min interval); this handler
// just reads the cached derp_health table.
//
// Query params:
//   - show_unavailable=1 — show all rows (including
//     degraded / unprobed). Default is 0 (filter
//     hides degraded rows so the operator sees only
//     useful healthy DERPs sorted by latency).
//
// POST /admin/derp/dashboard/refresh — force a fresh probe
// cycle and re-render the page.
//
// Live view, not policy enforcement. See internal/derphealth/
// for the probe + persistence logic.

package admin

import (
	"database/sql"
	"log"
	"net/http"
	"sort"
	"time"

	"skygate/internal/derphealth"
)

// GetAdminDerpDashboard renders the dashboard. Reads from
// derp_health; the cron has been writing there for at
// least one tick (or RunOnceNow has been called), so the
// page always has at least 1 row of fresh data.
func (s *Service) GetAdminDerpDashboard(w http.ResponseWriter, r *http.Request) {
	// 2026-08-31 (TD-18.2): the layout reads {{.UnreadCount}}
	// for the notification bell badge (B157). renderWithLayout
	// only auto-injects UnreadCount when c != nil. Pre-fix the
	// handler passed nil for c, so the template's `{{if gt
	// .UnreadCount 0}}` failed with "invalid type for
	// comparison" and the whole page (theme + body) was
	// unrendered. Now we extract the JWT claims from the
	// request via Backend.CurrentUser(r) like every other
	// admin handler (see internal/feature/admin/admin_pages.go
	// GetAdminAudit, internal/feature/admin/acl_import.go
	// GetAdminACLsImport, etc).
	c := s.Backend.CurrentUser(r)
	rows, err := s.dbc().QueryContext(r.Context(), `
		SELECT region_id, is_own, host, url, name, region_code, region_name,
		       locality, country, latency_ms, last_check, healthy,
		       last_error, probes_total, probes_failed
		  FROM derp_health
		 ORDER BY is_own DESC, region_id ASC
	`)
	if err != nil {
		log.Printf("derp dashboard: query: %v", err)
		s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
			map[string]any{"Error": "db query failed"})
		return
	}
	defer rows.Close()

	var all []derphealth.HealthRow
	for rows.Next() {
		var r derphealth.HealthRow
		var healthy, isOwn int
		var latency sql.NullInt64
		var lastCheck int64
		if err := rows.Scan(&r.RegionID, &isOwn, &r.Host, &r.URL,
			&r.Name, &r.RegionCode, &r.RegionName, &r.Locality, &r.Country,
			&latency, &lastCheck, &healthy, &r.LastError,
			&r.ProbesTotal, &r.ProbesFailed); err != nil {
			log.Printf("derp dashboard: scan: %v", err)
			continue
		}
		r.IsOwn = isOwn == 1
		r.Healthy = healthy == 1
		if latency.Valid {
			r.LatencyMs = int(latency.Int64)
		}
		r.LastCheck = time.Unix(lastCheck, 0).UTC()
		all = append(all, r)
	}

	// B228: query-param filter — drop degraded / unprobed
	// rows by default so the operator sees only useful
	// healthy DERPs sorted by latency. The pre-B228 page
	// showed 28+ "degraded, —" rows which buried the one
	// actually-healthy own DERP (the operator's exact
	// 2026-09-03 report).
	//
	// `?show_unavailable=1` opts in to the pre-B228 view
	// (useful for debugging a regional outage where the
	// operator wants to see WHICH regions are down).
	//
	// "Healthy" in this filter = (r.Healthy && r.LatencyMs > 0).
	// The LatencyMs > 0 guard is the "probe actually ran and
	// returned a real number" check; rows where the probe
	// failed show LatencyMs=0 (not a real measurement) and
	// should be hidden even though r.Healthy might be set
	// from a previous tick (defensive — don't surface stale
	// "healthy" claims).
	totalCount := len(all)
	showUnavailable := r.URL.Query().Get("show_unavailable") == "1"
	visible := all
	if !showUnavailable {
		visible = visible[:0]
		for _, r := range all {
			if r.Healthy && r.LatencyMs > 0 {
				visible = append(visible, r)
			}
		}
	}

	// Sort the FULL set: pre-B228 own-first + latency.
	// Kept verbatim so the ?show_unavailable=1 view is
	// byte-identical to the pre-B228 page (B228 is a
	// pure filter + re-sort on the healthy subset, NOT a
	// sort change on the full view).
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].IsOwn != all[j].IsOwn {
			return all[i].IsOwn
		}
		li, lj := all[i].LatencyMs, all[j].LatencyMs
		if li == 0 && lj == 0 {
			return all[i].RegionID < all[j].RegionID
		}
		if li == 0 {
			return false
		}
		if lj == 0 {
			return true
		}
		return li < lj
	})

	// Sort the VISIBLE set: primary = latency ASC (fastest
	// first), tiebreaker = IsOwn DESC (own DERP wins on
	// equal latency), then RegionID ASC for stable ordering
	// across reloads. Only applied when the filter is
	// active — the show_unavailable=1 view reads from
	// `all` (the pre-B228-sorted full slice).
	if !showUnavailable {
		sort.SliceStable(visible, func(i, j int) bool {
			li, lj := visible[i].LatencyMs, visible[j].LatencyMs
			if li != lj {
				return li < lj
			}
			if visible[i].IsOwn != visible[j].IsOwn {
				return visible[i].IsOwn
			}
			return visible[i].RegionID < visible[j].RegionID
		})
	}

	// Pick the "recommended" DERP from the FULL set (so the
	// recommended banner survives even when the filtered view
	// is empty — operator still sees "here's the best, but
	// it's currently degraded"). First healthy+probed, own
	// first (the pre-B228 behaviour).
	var recommendedID int
	for _, r := range all {
		if r.Healthy && r.LatencyMs > 0 {
			if r.IsOwn {
				recommendedID = r.RegionID
				break
			}
			if recommendedID == 0 {
				recommendedID = r.RegionID
			}
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
		map[string]any{
			"DERPs":            visible,
			"TotalCount":       totalCount,
			"VisibleCount":     len(visible),
			"ShowUnavailable":  showUnavailable,
			"Recommended":      recommendedID,
			"Refreshed":        time.Now().UTC(),
		})
}

// PostAdminDerpDashboardRefresh forces a fresh probe cycle
// (RunOnceNow) and re-renders the page. Used by the
// "Re-probe all" button. Bounded by ProbeAllTimeout so a
// slow probe doesn't block the page forever.
func (s *Service) PostAdminDerpDashboardRefresh(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	ctx := r.Context()
	results, err := derphealth.RunOnceNow(ctx, s.dbc(), nil)
	if err != nil {
		log.Printf("derp dashboard refresh: %v", err)
		s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
			map[string]any{"Error": "refresh failed: " + err.Error()})
		return
	}
	ok, bad := 0, 0
	for _, r := range results {
		if r.Healthy {
			ok++
		} else {
			bad++
		}
	}
	// Redirect back to the dashboard so the page re-renders
	// with the fresh data.
	http.Redirect(w, r, "/admin/derp/dashboard?refreshed=1&ok="+itoa(ok)+"&bad="+itoa(bad),
		http.StatusSeeOther)
}

// itoa is a tiny local helper to avoid pulling in strconv
// just for the redirect query string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
