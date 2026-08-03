package admin

// subnets.go — /admin/subnets page (flat overview of every
// user_subnets row, with status filter and per-status counts).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_subnets.go.
//
// Handler: GetAdminSubnets. Helpers: subnetsForOverview,
// SidecarLastSync, SidecarLastStats, formatSyncStats, langFromSyncStats,
// overviewRow type.

import (
	"net/http"
	"strings"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/sidecar"
	"skygate/internal/subnet"
)

// GetAdminSubnets renders /admin/subnets — a flat overview
// of every portal user that has a row in user_subnets, with
// the current status (pending / active / disabled) and the
// per-user CIDR. Status filter (?status=active|pending|disabled)
// narrows the list. Admin-only.
func (s *Service) GetAdminSubnets(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("status"))
	all, err := s.subnetsForOverview()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := make([]overviewRow, 0, len(all))
	for _, sub := range all {
		row := overviewRow{Subnet: sub}
		_ = s.DB.QueryRow(`SELECT username FROM portal_users WHERE id = $1`, sub.UserID).Scan(&row.Username)
		if row.Username != "" {
			row.DNSName = subnet.ComputeMagicDNSNames(row.Username).Sidecar
		}
		_ = s.DB.QueryRow(
			`SELECT COUNT(*) FROM node_owner_map WHERE user_id = $1 AND tag = 'tag:private'`,
			sub.UserID,
		).Scan(&row.DeviceCount)
		_ = s.DB.QueryRow(`
			SELECT COUNT(DISTINCT mm.mesh_id)
			  FROM mesh_members mm
			  JOIN meshes m ON m.id = mm.mesh_id
			 WHERE mm.user_id = $1 AND m.status = 'active'`, sub.UserID,
		).Scan(&row.MeshCount)
		_ = s.DB.QueryRow(
			`SELECT COUNT(*) FROM user_subnet_shares WHERE grantor_user_id = $1`,
			sub.UserID,
		).Scan(&row.SharesGranted)
		_ = s.DB.QueryRow(
			`SELECT COUNT(*) FROM user_subnet_shares WHERE grantee_user_id = $1`,
			sub.UserID,
		).Scan(&row.SharesReceived)
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
		filtered = rows
	}
	counts := map[string]int{
		"all":      len(rows),
		"pending":  0,
		"active":   0,
		"disabled": 0,
	}
	for _, r := range rows {
		counts[r.Subnet.Status]++
	}
	totals := map[string]int{
		"devices":         0,
		"meshes":          0,
		"shares_granted":  0,
		"shares_received": 0,
	}
	for _, r := range rows {
		totals["devices"] += r.DeviceCount
		totals["meshes"] += r.MeshCount
		totals["shares_granted"] += r.SharesGranted
		totals["shares_received"] += r.SharesReceived
	}
	s.Backend.RenderWithLayout(w, r, "admin/subnets.html", c, map[string]any{
		"Rows":      filtered,
		"Status":    filter,
		"Counts":    counts,
		"Totals":    totals,
		"LastSync":  s.sidecarLastSync(),
		"LastStats": s.sidecarLastStats(),
	})
}

// subnetsForOverview returns every user_subnets row.
func (s *Service) subnetsForOverview() ([]subnet.Subnet, error) {
	rows, err := s.DB.Query(`
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
		var sub subnet.Subnet
		if err := rows.Scan(
			&sub.ID, &sub.UserID, &sub.CIDR, &sub.Status, &sub.ControlPlaneURL,
			&sub.RouterNodeID, &sub.RouterContainerID, &sub.RouterHostname,
			&sub.CreatedAt, &sub.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// sidecarLastSync returns the last sync time of the sidecar
// Manager, or "" if no sync has run yet.
func (s *Service) sidecarLastSync() string {
	if s.Sidecar == nil {
		return ""
	}
	t := s.Sidecar.LastSync()
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05 MST")
}

// sidecarLastStats returns the last sync stats as a human-readable
// string ("scanned 5, approved 1, disabled 0, errors 0"). Empty
// when the manager hasn't run yet.
func (s *Service) sidecarLastStats() string {
	if s.Sidecar == nil {
		return ""
	}
	st := s.Sidecar.LastStats()
	if st.At.IsZero() {
		return ""
	}
	return formatSyncStats(st)
}

// formatSyncStats is a pure helper, exported as package-private
// so a future test can pin the string format. Takes the generic
// sidecar.SyncStats (imported via type assertion to avoid a tight
// import dependency).
func formatSyncStats(st sidecar.SyncStats) string {
	return i18n.Tf(langFromSyncStats(st.At),
		"admin.subnets.stats_summary",
		st.NodesScanned, st.StatusActivated, st.StatusDisabled, st.Errors)
}

// langFromSyncStats picks a language for the stats line based
// on the timestamp's wall clock (placeholder — always EN for
// now; the operator can change this when the i18n key is split
// into ruCatalog/enCatalog).
func langFromSyncStats(_ time.Time) string { return "en" }

// overviewRow is what the admin/subnets.html template iterates
// over. Wraps a Subnet + the joined portal_users.username +
// the auto-resolving MagicDNS FQDN (v0.18.0) + mesh/usage
// counters (v0.25.0).
type overviewRow struct {
	Subnet         subnet.Subnet
	Username       string
	DNSName        string
	DeviceCount    int
	MeshCount      int
	SharesGranted  int
	SharesReceived int
}
