// Package admin — services.go (v0.33.1.40 B92).
//
// /admin/services — the operator-facing status board for the
// integrations skygate depends on but does not own: headscale
// (control plane API), headplane (admin web UI), and the local
// Tailscale node. The page reads from the cached snapshot that
// the Availability Checker (internal/feature/healthz/availability.go)
// refreshes every 30s in a background goroutine — the page is
// O(1) and never blocks on a slow headscale.
//
// Auto-refresh: the template emits <meta http-equiv="refresh"
// content="30"> so the operator doesn't have to F5. The page is
// read-only (no forms), so a full reload is safe.
package admin

import (
	"fmt"
	"net/http"
	"time"

	"skygate/internal/feature/healthz"
)

// servicesView is the per-integration row passed to the template.
// It mirrors healthz.IntegrationStatus but adds display-friendly
// fields (LastCheckedDisplay, LatencyDisplay, DetailDisplay) so
// the template stays a thin renderer. Pre-formatted strings
// avoid inline {{if}} branches in the template.
type servicesView struct {
	ID                string
	Label             string
	URL               string
	OK                bool
	IsZero            bool
	StatusBadge       string // "ok" / "down" / "not_configured"
	BadgeClass        string // "green" / "red" / "gray"
	LastCheckedDisplay string
	LatencyDisplay    string
	DetailDisplay     string
	Error             string
}

// servicesPage is the data passed to admin/services.html.
type servicesPage struct {
	Integrations     []servicesView
	AllOK            bool
	GeneratedAt      string
	CheckIntervalSec int
	// standard renderWithLayout fields
	Page        string
	Title       string
	FlashSuccess string
	FlashError   string
}

// AdminServices renders /admin/services. Admin-only.
func (s *Service) AdminServices(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	page := s.buildServicesPage()
	s.Backend.RenderWithLayout(w, r, "admin/services.html", c, map[string]any{
		"Page":          "admin/services",
		"Title":         "Integration status",
		"State":         page,
		"FlashSuccess":  r.URL.Query().Get("ok"),
		"FlashError":    r.URL.Query().Get("err"),
		"Integrations":  page.Integrations,
		"AllOK":         page.AllOK,
		"GeneratedAt":   page.GeneratedAt,
		"CheckIntervalSec": page.CheckIntervalSec,
	})
}

// buildServicesPage assembles the servicesPage from the cached
// snapshot. When the Checker isn't wired (nil), the page shows
// a "checker not configured" row for each integration. This
// keeps the page useful for older v0.33.1.39 deployments that
// haven't pulled the v0.33.1.40 binary yet.
func (s *Service) buildServicesPage() *servicesPage {
	page := &servicesPage{
		Page:            "admin/services",
		Title:           "Integration status",
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		CheckIntervalSec: 30, // default; updated below if checker wired
	}
	if s.AvailabilityChecker == nil {
		// No checker — show a single "not configured" row.
		page.Integrations = []servicesView{{
			ID:          "none",
			Label:       "Availability checker",
			URL:         "(not configured)",
			StatusBadge: "not_configured",
			BadgeClass:  "gray",
			DetailDisplay: "v0.33.1.40 B92 not enabled in this build",
		}}
		return page
	}
	page.CheckIntervalSec = int(s.AvailabilityChecker.Interval.Seconds())
	snap := s.AvailabilityChecker.Snapshot()
	page.GeneratedAt = snap.GeneratedAt.Format(time.RFC3339)
	for _, integ := range snap.Integrations {
		page.Integrations = append(page.Integrations, servicesView{
			ID:                string(integ.ID),
			Label:             integ.Label,
			URL:               integ.URL,
			OK:                integ.OK,
			IsZero:            integ.IsZero(),
			StatusBadge:       statusBadge(integ),
			BadgeClass:        badgeClass(integ),
			LastCheckedDisplay: formatLastChecked(integ.LastChecked),
			LatencyDisplay:    formatLatency(integ.LatencyMS),
			DetailDisplay:     integ.Detail,
			Error:             integ.Error,
		})
	}
	page.AllOK = snap.AllOK()
	return page
}

// statusBadge returns "ok" / "down" / "not_configured" for the
// colored pill. The template uses these to look up
// badge-{green,red,gray} CSS classes.
func statusBadge(integ healthz.IntegrationStatus) string {
	if integ.IsZero() {
		return "not_configured"
	}
	if !integ.OK {
		return "down"
	}
	return "ok"
}

// badgeClass maps the status badge to a CSS class. Kept as a
// helper so the template can stay string-typed (no enum
// imports in HTML).
func badgeClass(integ healthz.IntegrationStatus) string {
	if integ.IsZero() {
		return "gray"
	}
	if !integ.OK {
		return "red"
	}
	return "green"
}

// formatLastChecked renders the timestamp as a short
// "Mon 15:04:05 UTC" or "(never)" string.
func formatLastChecked(t time.Time) string {
	if t.IsZero() {
		return "(never)"
	}
	return t.Format("2006-01-02 15:04:05 UTC")
}

// formatLatency renders the millisecond count as "23 ms" or
// "(n/a)" when the probe hasn't run.
func formatLatency(ms int64) string {
	if ms == 0 {
		return "(n/a)"
	}
	return fmt.Sprintf("%d ms", ms)
}
