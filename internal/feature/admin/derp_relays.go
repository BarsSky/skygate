// Package admin — derp_relays.go
//
// v1.3.17: DERP relay CRUD handlers (per-row add/edit/delete/
// toggle/test) for the /admin/derp/relays page. The earlier
// /admin/derp/config (v0.11.0 Этап 14 v14) had a single
// textarea + bundled checkbox; the operator wanted the
// exit-nodes-style per-row management so they could track
// region metadata, sort order, and enable / disable without
// editing a comma-separated string.
//
// Routes (registered in cmd/skygate/main.go):
//   GET  /admin/derp/relays         — list + per-row actions
//   POST /admin/derp/relays/add     — add new relay
//   POST /admin/derp/relays/edit    — edit existing relay
//   POST /admin/derp/relays/delete  — delete (rejects bundled)
//   POST /admin/derp/relays/toggle  — flip enabled flag
//   POST /admin/derp/relays/test    — per-row "Test connection"
//
// The /admin/derp/config page is still served for the
// deprecated bundled-only form (it's the v0.11.0 surface
// and v1.3.17 keeps it working — the bundled row in
// derp_relays is the single source of truth, and
// integrations.go's save handler now writes both the
// legacy global_settings keys AND the derp_relays table).
//
// 2026-08-13: v1.3.17.

package admin

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
)

// ---------- GET /admin/derp/relays ----------

// GetAdminDerpRelays renders the per-row DERP relay list.
// Auto-runs the one-shot backward-compat migration
// (db.AutoMigrateDerpRelays) on every page load — idempotent,
// gated by a "derp.relays_migrated"=1 global_settings marker.
//
// Renders admin/derp_relays.html with:
//   .Relays          []db.DerpRelay
//   .FlashSuccess    string (from ?ok=)
//   .FlashError      string (from ?err=)
//   .LastTestResult  *db.DerpRelay (id+latency_ms of the
//                    most recent /test POST, so the operator
//                    sees the test result inline)
func (s *Service) GetAdminDerpRelays(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := db.AutoMigrateDerpRelays(s.dbc()); err != nil {
		http.Error(w, "auto-migrate derp_relays: "+err.Error(),
			http.StatusInternalServerError)
		return
	}
	relays, err := db.ListDerpRelays(s.dbc())
	if err != nil {
		http.Error(w, "list derp_relays: "+err.Error(),
			http.StatusInternalServerError)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/derp_relays.html", c, map[string]any{
		"Relays":       relays,
		"MapStatus":    s.derpRelayMapStatuses(),
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// RelayMapStatus is one relay row's outcome in the map headscale fetches
// (B289) — the operator-visible half of the fix.
type RelayMapStatus struct {
	RegionID  int
	Host      string
	URL       string
	Bundled   bool
	Enabled   bool
	Published bool
	Via       string // address that answered the reachability probe
	Reason    string // why it is NOT published
}

var (
	mapStatusMu      sync.Mutex
	mapStatusCached  []RelayMapStatus
	mapStatusAt      time.Time
	mapStatusTTL     = 30 * time.Second
	mapStatusTimeout = 1500 * time.Millisecond
)

// derpRelayMapStatuses reports, per enabled row, whether
// `/admin/derp/relays/derpmap.json` will publish it and — when it will not — the
// reason and the addresses that were probed.
//
// WHY (live `skygate-host`, 2026-09-22): the operator's own derper was healthy
// and the map endpoint answered `{"Regions":[]}`, so no client ever used the
// local relay — and the ONLY place that said why was the journal, at debug-ish
// log level, three lines per headscale fetch. "Локальный DERP не работает" must
// be readable on the page that configures it. The list is probed with the SAME
// candidate/fallback logic the map endpoint uses, behind a 30s cache so opening
// the page cannot cost one dial timeout per dead row.
func (s *Service) derpRelayMapStatuses() []RelayMapStatus {
	mapStatusMu.Lock()
	if time.Since(mapStatusAt) < mapStatusTTL && mapStatusCached != nil {
		out := mapStatusCached
		mapStatusMu.Unlock()
		return out
	}
	mapStatusMu.Unlock()

	rows, err := s.dbc().Query(`SELECT region_id, hostname, COALESCE(url,''), COALESCE(is_bundled,0), COALESCE(enabled,0)
	                              FROM derp_relays ORDER BY is_bundled DESC, sort_order ASC, region_id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	probeHost := strings.TrimSpace(os.Getenv("SKYGATE_DERP_PROBE_HOST"))
	var out []RelayMapStatus
	for rows.Next() {
		var st RelayMapStatus
		var bundled, enabled int
		if err := rows.Scan(&st.RegionID, &st.Host, &st.URL, &bundled, &enabled); err != nil {
			continue
		}
		st.Bundled, st.Enabled = bundled == 1, enabled == 1
		switch {
		case !st.Enabled:
			st.Reason = "row is disabled"
		case isDerpMapURL(st.URL):
			st.Reason = "url is a derpmap document, not a relay node (headscale merges the public map itself)"
		default:
			port := publicDERPPortFromURL(st.URL)
			candidates := derpReachabilityCandidates(s.dbc(), st.Host, probeHost)
			// B289.1: dial the candidate ADDRESS, present the relay's public
			// hostname as SNI (derper's manual certmode resolves the cert by SNI).
			reach, via := probeDERPNodeReachableAny(derpProbeTargetsFor(candidates, st.Host), port, mapStatusTimeout)
			if reach.Reachable {
				st.Published = true
				st.Via = via
			} else {
				st.Reason = fmt.Sprintf("unreachable (probed %s): %s", strings.Join(candidates, ", "), reach.Err)
			}
		}
		out = append(out, st)
	}
	mapStatusMu.Lock()
	mapStatusCached, mapStatusAt = out, time.Now()
	mapStatusMu.Unlock()
	return out
}

// ---------- POST /admin/derp/relays/add ----------

// PostAdminDerpRelaysAdd handles the "Add new DERP relay"
// form. Admin-only. Redirects back to /admin/derp/relays
// with a flash message on success / error.
func (s *Service) PostAdminDerpRelaysAdd(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form",
			http.StatusFound)
		return
	}
	row := db.DerpRelay{
		Hostname:   strings.TrimSpace(r.FormValue("hostname")),
		URL:        strings.TrimSpace(r.FormValue("url")),
		RegionID:   db.MustParseInt(r.FormValue("region_id")),
		RegionCode: strings.TrimSpace(r.FormValue("region_code")),
		RegionName: strings.TrimSpace(r.FormValue("region_name")),
		SortOrder:  db.MustParseInt(r.FormValue("sort_order")),
		Notes:      strings.TrimSpace(r.FormValue("notes")),
		Enabled:    r.FormValue("enabled") == "1",
	}
	if row.URL == "" {
		http.Redirect(w, r, "/admin/derp/relays?err=url_required",
			http.StatusFound)
		return
	}
	if _, err := db.AddDerpRelay(s.dbc(), row); err != nil {
		msg := urlMsg(err)
		http.Redirect(w, r, "/admin/derp/relays?err="+msg,
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_relay.add",
		fmt.Sprintf("hostname=%s url=%s region_id=%d",
			row.Hostname, row.URL, row.RegionID))
	http.Redirect(w, r, "/admin/derp/relays?ok=added", http.StatusFound)
}

// ---------- POST /admin/derp/relays/edit ----------

// PostAdminDerpRelaysEdit handles the inline "Edit" form
// for an existing row. The form is rendered per-row in
// derp_relays.html (one <form> per row, with hidden id);
// the operator clicks "Save" to submit.
func (s *Service) PostAdminDerpRelaysEdit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form",
			http.StatusFound)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=bad_id",
			http.StatusFound)
		return
	}
	row := db.DerpRelay{
		ID:         id,
		Hostname:   strings.TrimSpace(r.FormValue("hostname")),
		URL:        strings.TrimSpace(r.FormValue("url")),
		RegionID:   db.MustParseInt(r.FormValue("region_id")),
		RegionCode: strings.TrimSpace(r.FormValue("region_code")),
		RegionName: strings.TrimSpace(r.FormValue("region_name")),
		SortOrder:  db.MustParseInt(r.FormValue("sort_order")),
		Notes:      strings.TrimSpace(r.FormValue("notes")),
		Enabled:    r.FormValue("enabled") == "1",
	}
	if row.URL == "" {
		http.Redirect(w, r, "/admin/derp/relays?err=url_required",
			http.StatusFound)
		return
	}
	if err := db.UpdateDerpRelay(s.dbc(), row); err != nil {
		msg := urlMsg(err)
		http.Redirect(w, r, "/admin/derp/relays?err="+msg,
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_relay.edit",
		fmt.Sprintf("id=%d hostname=%s url=%s",
			row.ID, row.Hostname, row.URL))
	http.Redirect(w, r, "/admin/derp/relays?ok=updated", http.StatusFound)
}

// ---------- POST /admin/derp/relays/delete ----------

// PostAdminDerpRelaysDelete removes a row. The bundled
// row is undeletable (db.DeleteDerpRelay returns
// ErrDerpRelayBundledUndeletable) — the operator should
// toggle its enabled flag instead, which is the on/off
// switch for the bundled derper container.
func (s *Service) PostAdminDerpRelaysDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form",
			http.StatusFound)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=bad_id",
			http.StatusFound)
		return
	}
	if err := db.DeleteDerpRelay(s.dbc(), id); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err="+urlMsg(err),
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_relay.delete",
		fmt.Sprintf("id=%d", id))
	http.Redirect(w, r, "/admin/derp/relays?ok=deleted", http.StatusFound)
}

// ---------- POST /admin/derp/relays/toggle ----------

// PostAdminDerpRelaysToggle flips the enabled flag for
// one row. Used both for the per-row "Disable / Enable"
// button on external relays AND as the on/off switch for
// the bundled derper container.
func (s *Service) PostAdminDerpRelaysToggle(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form",
			http.StatusFound)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=bad_id",
			http.StatusFound)
		return
	}
	row, err := db.ToggleDerpRelayEnabled(s.dbc(), id)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err="+urlMsg(err),
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_relay.toggle",
		fmt.Sprintf("id=%d enabled=%t", row.ID, row.Enabled))
	http.Redirect(w, r, "/admin/derp/relays?ok=toggled", http.StatusFound)
}

// ---------- POST /admin/derp/relays/test ----------

// PostAdminDerpRelaysTest runs the same 5s probe as the
// /admin/derp/config "Test all" button, but for ONE row.
// Result is rendered as a flash via ?ok= or ?err= — the
// page reads ?ok= and shows a per-row "X ms ✓" badge.
func (s *Service) PostAdminDerpRelaysTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=parse_form",
			http.StatusFound)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err=bad_id",
			http.StatusFound)
		return
	}
	row, err := db.GetDerpRelay(s.dbc(), id)
	if err != nil {
		http.Redirect(w, r, "/admin/derp/relays?err="+urlMsg(err),
			http.StatusFound)
		return
	}
	if row.URL == "" {
		// Bundled row — the URL is generated at apply
		// time. The operator can run Apply to refresh,
		// but we can also report the local probe target
		// (the derper's debug endpoint).
		http.Redirect(w, r,
			"/admin/derp/relays?err=bundled_has_no_url",
			http.StatusFound)
		return
	}
	result := probeDerpURL(row.URL)
	msg := fmt.Sprintf("test_%d=%dms", row.ID, result.LatencyMS)
	if !result.OK {
		http.Redirect(w, r,
			"/admin/derp/relays?err="+urlMsgFromString(result.Err),
			http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "derp_relay.test",
		fmt.Sprintf("id=%d url=%s ok=true latency_ms=%d",
			row.ID, row.URL, result.LatencyMS))
	http.Redirect(w, r, "/admin/derp/relays?ok="+msg,
		http.StatusFound)
}

// ---------- helpers ----------

// urlMsg returns a short, URL-safe error code for the
// ?err= flash parameter. The template looks up the
// localized message via catalog_derp.go. Long messages
// are truncated to keep the redirect URL short.
func urlMsg(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "duplicate"):
		return "duplicate_url"
	case strings.Contains(s, "bundled") && strings.Contains(s, "exists"):
		return "bundled_exists"
	case strings.Contains(s, "bundled") && strings.Contains(s, "deletable"):
		return "bundled_undeletable"
	case strings.Contains(s, "not found"):
		return "not_found"
	case strings.Contains(s, "url is required"):
		return "url_required"
	case strings.Contains(s, "parse"):
		return "bad_id"
	}
	return "internal"
}

// urlMsgFromString is the same as urlMsg but for the
// probeDerpURL error path (which returns a string, not
// an error). Just URL-encodes the first 80 chars.
func urlMsgFromString(s string) string {
	if len(s) > 80 {
		s = s[:80]
	}
	return "probe_" + strings.ReplaceAll(s, " ", "_")
}
