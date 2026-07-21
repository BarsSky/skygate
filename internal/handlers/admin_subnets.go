package handlers

import (
	"net/http"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/i18n"
	"skygate/internal/sidecar"
	"skygate/internal/subnet"
)

// GetAdminSubnets renders /admin/subnets — a flat
// overview of every portal user that has a row in
// user_subnets, with the current status (pending /
// active / disabled) and the per-user CIDR. Status
// filter (?status=active|pending|disabled) narrows
// the list. The page is the v0.16.10 "all subnets
// at a glance" view that complements the per-user
// /admin/users/{id}/subnet detail page.
//
// v0.16.10. 2026-07-17.
func (a *App) GetAdminSubnets(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("status"))
	// Pull every row. The list is small (≤ portal user
	// count) so a single SELECT is fine; pagination can
	// wait for a v0.17.0 if the tailnet ever has more
	// than ~50 subnets.
	all, err := a.subnetsForOverview()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := make([]overviewRow, 0, len(all))
	for _, s := range all {
		row := overviewRow{
			Subnet: s,
		}
		// Look up the portal username for the row.
		// Left join in SQL would be cleaner; doing it
		// in Go keeps the manager API surface narrow.
		// 2026-07-17: v0.18.0 — also compute the
		// auto-resolving MagicDNS FQDN for the user
		// (skygate-subnet-<username>.<base-domain>).
		_ = a.DB.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, s.UserID).Scan(&row.Username)
		if row.Username != "" {
			row.DNSName = subnet.ComputeMagicDNSNames(row.Username).Sidecar
		}
		rows = append(rows, row)
	}
	var filtered []overviewRow
	switch filter {
	case "", "all":
		filtered = rows
	case "pending", "active", "disabled":
		for _, r := range rows {
			if r.Subnet.Status == filter {
				filtered = append(filtered, r)
			}
		}
	default:
		// Unknown filter — show all (don't 400 the page).
		filtered = rows
	}
	// Compute per-status counts (always, for the filter chips).
	counts := map[string]int{
		"all":      len(rows),
		"pending":  0,
		"active":   0,
		"disabled": 0,
	}
	for _, r := range rows {
		counts[r.Subnet.Status]++
	}
	a.renderWithLayout(w, r, "admin/subnets.html", c, map[string]any{
		"Rows":       filtered,
		"Status":     filter,
		"Counts":     counts,
		"LastSync":   a.SidecarLastSync(),
		"LastStats":  a.SidecarLastStats(),
	})
}

// subnetsForOverview returns every user_subnets row.
// Joins with portal_users via the in-Go lookup in
// GetAdminSubnets (above) so the SQL stays in the
// subnet package.
func (a *App) subnetsForOverview() ([]subnet.Subnet, error) {
	rows, err := a.DB.Query(`
		SELECT id, user_id, cidr, status, control_plane_url,
		       router_node_id, router_container_id, router_hostname,
		       created_at, updated_at
		  FROM user_subnets
		 ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subnet.Subnet
	for rows.Next() {
		var s subnet.Subnet
		if err := rows.Scan(
			&s.ID, &s.UserID, &s.CIDR, &s.Status, &s.ControlPlaneURL,
			&s.RouterNodeID, &s.RouterContainerID, &s.RouterHostname,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// overviewRow is what the admin/subnets.html template
// iterates over. Wraps a Subnet + the joined
// portal_users.username (avoids the template having
// to do its own DB lookup) + the auto-resolving
// MagicDNS FQDN (v0.18.0).
type overviewRow struct {
	Subnet   subnet.Subnet
	Username string
	DNSName  string
}

// SidecarLastSync returns the last sync time of the
// sidecar Manager, or zero if no sync has run yet.
// The admin/subnets page renders this as "last
// auto-approve sync: 12:34:56" so the operator
// knows the auto-approver is alive.
func (a *App) SidecarLastSync() string {
	if a.Sidecar == nil {
		return ""
	}
	t := a.Sidecar.LastSync()
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05 MST")
}

// SidecarLastStats returns the last sync stats as a
// human-readable string ("scanned 5, approved 1,
// disabled 0, errors 0"). Empty when the manager
// hasn't run yet.
func (a *App) SidecarLastStats() string {
	if a.Sidecar == nil {
		return ""
	}
	s := a.Sidecar.LastStats()
	if s.At.IsZero() {
		return ""
	}
	return formatSyncStats(s)
}

// formatSyncStats — pure helper, exported as package-private
// so a future test can pin the string format. Takes the
// generic sidecar.SyncStats (imported via type assertion
// to avoid a tight import dependency).
func formatSyncStats(s sidecar.SyncStats) string {
	return i18n.Tf(langFromSyncStats(s.At),
		"admin.subnets.stats_summary",
		s.NodesScanned, s.StatusActivated, s.StatusDisabled, s.Errors)
}

// langFromSyncStats picks a language for the stats line
// based on the timestamp's wall clock (a placeholder —
// the real i18n key would be selected by env). Always
// EN for now; the operator can change this when the
// i18n key is split into ruCatalog/enCatalog.
func langFromSyncStats(_ time.Time) string { return "en" }

// Suppress unused-import warning when the package is
// built without auth (the helper isn't called by the
// overview page but auth.Claims is referenced via
// `c *auth.Claims` in the renderWithLayout call).
var _ = auth.Claims{}
