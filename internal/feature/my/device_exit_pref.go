// Package my — device_exit_pref.go owns the two
// per-device preferred exit-node POST endpoints
// (v0.28.4):
//
//   * POST /my/devices/preferred-exit   (self-service, all users)
//   * POST /admin/devices/preferred-exit (admin override, any device)
//
// Both write to the same table. The user endpoint
// is scoped to the caller's own devices; the admin
// endpoint takes a user_id in the form body and can
// target any user's device.
//
// refactor-v0.30 Phase B step 5d (2026-07-29):
// moved from internal/handlers/handlers_device_exit_pref.go.
// The two POST handlers + the callerOwnsDevice
// helper + the errToQuery helper + itoa64 all live
// on *Service now. errToQuery is shared with
// feature/my/exit_nodes.go (both files are in the
// same package) — the legacy handlers_device_exit_pref.go
// copy was a temporary duplicate that this move
// resolves (one fewer copy of the helper in the
// repo).
package my

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// PostMyDevicePreferredExit sets (or clears) the
// caller's preferred exit-node for a specific device.
// The caller MUST own the device (we look it up by
// hostname in node_owner_map, filtered to the
// caller's user_id).
//
// Form fields:
//   * hostname — the device's hostname (case-insensitive
//                match; we lowercase before the lookup
//                to match the v0.28.0 backfill convention)
//   * tag      — the exit-node tag (e.g.
//                "tag:exit-relay-3"); empty clears
//                the override (device falls back to
//                per-user pref, if any)
//
// After the DB write, an ACL re-apply pushes the
// new per-device grant to headscale.
//
// 2026-07-25: v0.28.4.
func (s *Service) PostMyDevicePreferredExit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	tag := strings.TrimSpace(r.FormValue("tag"))
	if hostname == "" {
		http.Error(w, "hostname required", 400)
		return
	}
	// 2026-07-25: v0.28.5 — strict pinning is opt-in
	// (same model as the per-user pref). Default OFF
	// for Android compatibility.
	viaEnabled := r.FormValue("via") == "1"
	// Validate the caller owns this device. The
	// node_owner_map hostname column is populated by
	// the v0.28.0 backfill at /my/devices load time.
	// If the device isn't in the map, the caller's
	// claim fails (no device-by-hostname impersonation).
	if !s.callerOwnsDevice(s.DB, c.UserID, hostname) {
		http.Error(w, "device not found or not owned by you", 403)
		return
	}
	if err := db.SetDeviceExitNodePref(s.DB, c.UserID, hostname, tag, c.UserID, viaEnabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "my_device_preferred_exit_set",
		"hostname="+hostname+" tag="+tag+" via="+strconv.FormatBool(viaEnabled))
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	res := acl.ApplyACLPipelineForPlane(s.DB, s.Backend.HSForUserFn(c.UserID), "", nil, c.Username,
		"my_device_preferred_exit_set hostname="+hostname+" tag="+tag, viaFlag)
	if !res.Applied {
		http.Redirect(w, r, "/my/devices?err="+errToQuery(res.Err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/my/devices?ok=1", http.StatusSeeOther)
}

// PostAdminDevicePreferredExit sets (or clears) the
// preferred exit-node for a specific (user, device)
// pair. The admin can target any user's device
// (operator-driven exit-node assignment, e.g. pin
// workstation-3 → relay-3 while admin's default stays
// relay-1).
//
// Form fields:
//   * user_id  — the device's owner
//   * hostname — the device's hostname (lowercased)
//   * tag      — the exit-node tag; empty clears
//
// 2026-07-25: v0.28.4.
//
// Note: this admin override is a *my/*-package
// surface but is admin-only. The /admin/devices
// admin page itself still lives in feature/admin/;
// only the POST endpoint moved here. A future refactor
// can move PostAdminDevicePreferredExit to
// feature/admin/ alongside the /admin/devices page.
func (s *Service) PostAdminDevicePreferredExit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if !c.IsAdmin {
		http.Error(w, "admin only", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	var userID int64
	if v := r.FormValue("user_id"); v != "" {
		var n int64
		for i := 0; i < len(v); i++ {
			if v[i] < '0' || v[i] > '9' {
				http.Error(w, "bad user_id", 400)
				return
			}
			n = n*10 + int64(v[i]-'0')
		}
		userID = n
	}
	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	tag := strings.TrimSpace(r.FormValue("tag"))
	viaEnabled := r.FormValue("via") == "1"
	if userID == 0 || hostname == "" {
		http.Error(w, "user_id and hostname required", 400)
		return
	}
	if err := db.SetDeviceExitNodePref(s.DB, userID, hostname, tag, c.UserID, viaEnabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "admin_device_preferred_exit_set",
		"target_user_id="+itoa64(userID)+" hostname="+hostname+" tag="+tag+" via="+strconv.FormatBool(viaEnabled))
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	res := acl.ApplyACLPipelineForPlane(s.DB, s.Backend.HSForUserFn(userID), "", nil, c.Username,
		"admin_device_preferred_exit_set target_user_id="+itoa64(userID)+" hostname="+hostname+" tag="+tag, viaFlag)
	if !res.Applied {
		http.Redirect(w, r, "/admin/devices?err="+errToQuery(res.Err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/devices?ok=1", http.StatusSeeOther)
}

// callerOwnsDevice returns true iff the caller has a
// node_owner_map row for (user_id, lowercased
// hostname). The check is intentionally strict — a
// user can only set prefs for devices they actually
// own (per the v0.28.0 backfill). Impersonation
// would let alice change bob's device pref via the
// /my/ endpoint, which is exactly what we don't want.
//
// 2026-07-25: v0.28.4.
func (s *Service) callerOwnsDevice(d *sql.DB, userID int64, lowerHostname string) bool {
	if lowerHostname == "" {
		return false
	}
	// node_owner_map.hostname is stored as the
	// headscale givenName (e.g. "MSI" for the MSI
	// device). The v0.28.0 tag is the lowercased
	// form. We do a case-insensitive match here.
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM node_owner_map WHERE tagged_by_user_id = `+db.PlaceholdersList(1)+` AND LOWER(hostname) = `+db.PlaceholdersList(1),
		userID, lowerHostname,
	).Scan(&n)
	if err != nil {
		return false
	}
	return n > 0
}

// itoa64 is a small helper to format int64 without
// pulling in strconv for one call site.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
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
