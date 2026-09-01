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
	"strconv"
	"strings"

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
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
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
