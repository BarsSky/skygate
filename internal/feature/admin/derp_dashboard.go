// File: internal/feature/admin/derp_dashboard.go
// B189 (v1.5.2) — DERP Health Dashboard.
//
// Renders /admin/derp/dashboard with one row per known
// DERP server (own + Tailscale's 28 public regions) showing
// the most recent probe's latency, health, and error.
// Probing happens in the background (derphealth.StartCron
// in cmd/skygate/main.go, 5-min interval); this handler
// just reads the cached derp_health table.
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
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT region_id, is_own, host, url, region_code, region_name,
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
			&r.RegionCode, &r.RegionName, &r.Locality, &r.Country,
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

	// Sort: own first, then by latency (fastest first within
	// each group; NULL/unprobed last within each group).
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

	// Pick the "recommended" DERP: the first healthy own
	// DERP, or the first healthy public DERP if no own is
	// healthy.
	var recommendedID int
	for _, r := range all {
		if r.Healthy && r.LatencyMs > 0 {
			recommendedID = r.RegionID
			break
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
		map[string]any{
			"DERPs":        all,
			"Recommended":  recommendedID,
			"Refreshed":    time.Now().UTC(),
		})
}

// PostAdminDerpDashboardRefresh forces a fresh probe cycle
// (RunOnceNow) and re-renders the page. Used by the
// "Re-probe all" button. Bounded by ProbeAllTimeout so a
// slow probe doesn't block the page forever.
func (s *Service) PostAdminDerpDashboardRefresh(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	ctx := r.Context()
	results, err := derphealth.RunOnceNow(ctx, s.DB, nil)
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
