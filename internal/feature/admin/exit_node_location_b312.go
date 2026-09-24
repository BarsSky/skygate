// internal/feature/admin/exit_node_location_b312.go — B312 (v1.5.77).
//
// The operator's half of the location feature: a relay's place can be typed in by
// hand (and handed back to the automatic lookup), because the automatic detection
// needs a public address and a working network — and neither is guaranteed. A manual
// value is never overwritten by the lookup, which is the whole point: the operator's
// answer about where their own machine sits beats any API's.
package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/geoloc"
)

// PostAdminExitNodeLocation stores (or clears) one relay's location.
//
// Form fields: hostname, label, country, lat, lon. An empty label AND country means
// "clear": the relay goes back to the automatic lookup (and to "unknown" when that is
// off). Refusals are flashes, never a raw error page (the operator-facing convention
// of this page), and the audit row says who decided the relay is where.
func (s *Service) PostAdminExitNodeLocation(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := "en"
	if s.I18n != nil {
		lang = s.I18n.LangFromRequest(r)
	}
	translate := func(key string, args ...interface{}) string {
		// A Service built without a catalog (unit tests, or a partially wired boot)
		// must still answer with the key instead of panicking on a nil catalog.
		if s.I18n == nil {
			return key
		}
		if len(args) > 0 {
			return s.I18n.Tf(lang, key, args...)
		}
		return s.I18n.T(lang, key)
	}
	flash := func(key string, args ...interface{}) {
		http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(translate(key, args...)), http.StatusSeeOther)
	}
	flashErr := func(key string, args ...interface{}) {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(translate(key, args...)), http.StatusSeeOther)
	}

	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		flashErr("exit_nodes.location.err_hostname")
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	country := strings.TrimSpace(r.FormValue("country"))
	latRaw := strings.TrimSpace(r.FormValue("lat"))
	lonRaw := strings.TrimSpace(r.FormValue("lon"))

	// Coordinates are optional; when given they must parse, because a typo would
	// silently become "no coordinates" and quietly disable the distance tie-break.
	lat, lon, hasCoords, coordErr := parseLocationCoords(latRaw, lonRaw)
	if coordErr != "" {
		flashErr(coordErr)
		return
	}
	if len(label) > 120 || len(country) > 120 {
		flashErr("exit_nodes.location.err_toolong")
		return
	}
	latStored, lonStored := geoloc.FormatCoords(lat, lon, hasCoords)
	n, err := db.SetExitLocationManual(s.dbc(), hostname, label, country, latStored, lonStored)
	if err != nil {
		flashErr("error.db")
		return
	}
	if n == 0 {
		flashErr("exit_nodes.location.err_unknown_relay", hostname)
		return
	}
	action := "exit_node_location_set"
	detail := hostname + " -> " + strings.TrimSpace(label+" "+country)
	if label == "" && country == "" {
		action = "exit_node_location_cleared"
		detail = hostname + " -> auto"
	}
	s.Backend.Audit(c.UserID, c.Username, action, detail)
	if label == "" && country == "" {
		flash("exit_nodes.location.cleared", hostname)
		return
	}
	flash("exit_nodes.location.saved", hostname)
}

// parseLocationCoords validates the optional coordinate pair. It returns the values,
// whether they are usable, and a (translated) error key when they are not.
func parseLocationCoords(latRaw, lonRaw string) (float64, float64, bool, string) {
	if latRaw == "" && lonRaw == "" {
		return 0, 0, false, ""
	}
	if latRaw == "" || lonRaw == "" {
		return 0, 0, false, "exit_nodes.location.err_coords_partial"
	}
	lat, err1 := strconv.ParseFloat(latRaw, 64)
	lon, err2 := strconv.ParseFloat(lonRaw, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false, "exit_nodes.location.err_coords_format"
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false, "exit_nodes.location.err_coords_range"
	}
	return lat, lon, true, ""
}
