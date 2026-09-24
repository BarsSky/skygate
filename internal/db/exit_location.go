// exit_location.go — the exit_servers location columns (B312).
//
// A relay's location is written by exactly two paths and never in between:
//
//   - the operator (source = "manual"), which wins and is never overwritten;
//   - the automatic lookup (source = "auto"), which fills a row only while it is
//     manual-free and due.
//
// Every read tolerates an empty row: "location unknown" is a first-class answer, and
// the pages say so instead of rendering a blank that looks like a bug.
package db

import (
	"database/sql"
	"strings"
	"time"
)

// ExitLocation is one relay's place on the map as stored.
type ExitLocation struct {
	Hostname  string
	Label     string
	Country   string
	LatRaw    string
	LonRaw    string
	Source    string // "manual" | "auto" | ""
	CheckedAt int64  // unix; 0 = never auto-checked
}

// Known reports whether anything is known about this relay's place.
func (l ExitLocation) Known() bool {
	return strings.TrimSpace(l.Label) != "" || strings.TrimSpace(l.Country) != ""
}

// Manual reports whether the operator set this value by hand.
func (l ExitLocation) Manual() bool { return strings.EqualFold(strings.TrimSpace(l.Source), "manual") }

const qSelectExitLocation = `SELECT COALESCE(location_label,''), COALESCE(location_country,''),
       COALESCE(location_lat,''), COALESCE(location_lon,''), COALESCE(location_source,''), COALESCE(location_checked_at,0)
  FROM exit_servers WHERE hostname = $1 LIMIT 1`

// GetExitLocation reads one relay's stored location. A missing row is not an error —
// it is an unknown location.
func GetExitLocation(d *sql.DB, hostname string) (ExitLocation, error) {
	out := ExitLocation{Hostname: strings.TrimSpace(hostname)}
	if d == nil || out.Hostname == "" {
		return out, nil
	}
	err := d.QueryRow(qSelectExitLocation, out.Hostname).
		Scan(&out.Label, &out.Country, &out.LatRaw, &out.LonRaw, &out.Source, &out.CheckedAt)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

// ListExitLocations reads every relay's location, keyed by the hostname as stored
// (lower-cased), so the page and the assignment engine share one read.
func ListExitLocations(d *sql.DB) map[string]ExitLocation {
	out := map[string]ExitLocation{}
	if d == nil {
		return out
	}
	rows, err := d.Query(`SELECT hostname, COALESCE(location_label,''), COALESCE(location_country,''),
	                             COALESCE(location_lat,''), COALESCE(location_lon,''),
	                             COALESCE(location_source,''), COALESCE(location_checked_at,0)
	                        FROM exit_servers`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var l ExitLocation
		if err := rows.Scan(&l.Hostname, &l.Label, &l.Country, &l.LatRaw, &l.LonRaw, &l.Source, &l.CheckedAt); err != nil {
			continue
		}
		l.Hostname = strings.TrimSpace(l.Hostname)
		out[strings.ToLower(l.Hostname)] = l
	}
	return out
}

// SetExitLocationAuto stores an automatically detected location. It deliberately
// refuses to touch a manual row (the operator's answer beats a lookup) and refuses an
// empty detection, so a failed lookup cannot erase a good value — but it DOES record
// the attempt time, which is what keeps the refresh cheap.
func SetExitLocationAuto(d *sql.DB, hostname, label, country, lat, lon string, checkedAt int64) error {
	h := strings.TrimSpace(hostname)
	if d == nil || h == "" {
		return nil
	}
	if checkedAt == 0 {
		checkedAt = time.Now().Unix()
	}
	if strings.TrimSpace(label) == "" && strings.TrimSpace(country) == "" {
		_, err := d.Exec(`UPDATE exit_servers SET location_checked_at = $1
		                   WHERE hostname = $2 AND COALESCE(location_source,'') <> 'manual'`, checkedAt, h)
		return err
	}
	_, err := d.Exec(`UPDATE exit_servers
	                     SET location_label = $1, location_country = $2, location_lat = $3,
	                         location_lon = $4, location_source = 'auto', location_checked_at = $5
	                   WHERE hostname = $6 AND COALESCE(location_source,'') <> 'manual'`,
		label, country, lat, lon, checkedAt, h)
	return err
}

// SetExitLocationManual stores the operator's own answer (empty label = clear back to
// unknown/auto). Returns the number of rows touched so the caller can tell a typo
// (no such relay) from a save.
func SetExitLocationManual(d *sql.DB, hostname, label, country, lat, lon string) (int64, error) {
	h := strings.TrimSpace(hostname)
	if d == nil || h == "" {
		return 0, nil
	}
	source := "manual"
	if strings.TrimSpace(label) == "" && strings.TrimSpace(country) == "" {
		// Clearing: hand the relay back to the automatic lookup.
		source = ""
	}
	res, err := d.Exec(`UPDATE exit_servers
	                       SET location_label = $1, location_country = $2, location_lat = $3,
	                           location_lon = $4, location_source = $5, location_checked_at = 0
	                     WHERE hostname = $6`, label, country, lat, lon, source, h)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ExitLocationCandidate is one relay the automatic lookup may visit.
type ExitLocationCandidate struct {
	Hostname   string
	SSHTarget  string
	TailscaleI string
	Location   ExitLocation
}

// ExitServersDueForLocation returns the relays whose location is due for a lookup:
// never checked, or checked longer ago than refreshSec, and not pinned by hand. The
// operator's manual rows are excluded here (not filtered later), so the lookup never
// even asks about them.
func ExitServersDueForLocation(d *sql.DB, now int64, refreshSec int64) []ExitLocationCandidate {
	if d == nil {
		return nil
	}
	cutoff := now - refreshSec
	rows, err := d.Query(`SELECT hostname, COALESCE(ssh_target,''), COALESCE(tailscale_ip,''),
	                             COALESCE(location_label,''), COALESCE(location_country,''),
	                             COALESCE(location_lat,''), COALESCE(location_lon,''),
	                             COALESCE(location_source,''), COALESCE(location_checked_at,0)
	                        FROM exit_servers
	                       WHERE COALESCE(location_source,'') <> 'manual'
	                         AND (COALESCE(location_checked_at,0) = 0 OR COALESCE(location_checked_at,0) < $1)
	                       ORDER BY hostname`, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ExitLocationCandidate
	for rows.Next() {
		var c ExitLocationCandidate
		if err := rows.Scan(&c.Hostname, &c.SSHTarget, &c.TailscaleI, &c.Location.Label, &c.Location.Country,
			&c.Location.LatRaw, &c.Location.LonRaw, &c.Location.Source, &c.Location.CheckedAt); err != nil {
			continue
		}
		c.Location.Hostname = strings.TrimSpace(c.Hostname)
		out = append(out, c)
	}
	return out
}
