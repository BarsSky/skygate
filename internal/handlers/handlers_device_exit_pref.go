package handlers

// handlers_device_exit_pref.go — POST handlers for
// per-device preferred exit-node (v0.28.4).
//
// 2026-07-25: v0.28.4 lets a user pin a specific device
// to a different exit-node than their per-user default.
// This file owns the two POST endpoints that mutate the
// device_exit_node_prefs table:
//
//   * POST /my/devices/preferred-exit   (self-service, all users)
//   * POST /admin/devices/preferred-exit (admin override, any device)
//
// Both write to the same table. The user endpoint is
// scoped to the caller's own devices; the admin endpoint
// takes a user_id in the form body and can target any
// user's device.

import (
	"net/http"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// PostMyDevicePreferredExit sets (or clears) the caller's
// preferred exit-node for a specific device. The caller
// MUST own the device (we look it up by hostname in
// node_owner_map, filtered to the caller's user_id).
//
// Form fields:
//   * hostname — the device's hostname (case-insensitive
//                match; we lowercase before the lookup
//                to match the v0.28.0 backfill convention)
//   * tag      — the exit-node tag (e.g.
//                "tag:exit-relay-3"); empty clears the
//                override (device falls back to per-user
//                pref, if any)
//
// After the DB write, an ACL re-apply pushes the new
// per-device grant to headscale.
//
// 2026-07-25: v0.28.4.
func (a *App) PostMyDevicePreferredExit(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	// Validate the caller owns this device. The
	// node_owner_map hostname column is populated by the
	// v0.28.0 backfill at /my/devices load time. If
	// the device isn't in the map, the caller's claim
	// fails (no device-by-hostname impersonation).
	if !a.callerOwnsDevice(r, c.UserID, hostname) {
		http.Error(w, "device not found or not owned by you", 403)
		return
	}
	if err := db.SetDeviceExitNodePref(a.DB, c.UserID, hostname, tag, c.UserID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "my_device_preferred_exit_set",
		"hostname="+hostname+" tag="+tag)
	res := acl.ApplyACLPipelineForPlane(a.DB, a.HSForUser(c.UserID), "", nil, c.Username,
		"my_device_preferred_exit_set hostname="+hostname+" tag="+tag, a.Cfg.ACLWithViaEnabled)
	if !res.Applied {
		http.Redirect(w, r, "/my/devices?err="+errToQuery(res.Err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/my/devices?ok=1", http.StatusSeeOther)
}

// PostAdminDevicePreferredExit sets (or clears) the
// preferred exit-node for a specific (user, device) pair.
// The admin can target any user's device (operator-driven
// exit-node assignment, e.g. pin workstation-3 → relay-3 while
// admin's default stays relay-1).
//
// Form fields:
//   * user_id  — the device's owner
//   * hostname — the device's hostname (lowercased)
//   * tag      — the exit-node tag; empty clears
//
// 2026-07-25: v0.28.4.
func (a *App) PostAdminDevicePreferredExit(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
		// parse int64
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
	if userID == 0 || hostname == "" {
		http.Error(w, "user_id and hostname required", 400)
		return
	}
	if err := db.SetDeviceExitNodePref(a.DB, userID, hostname, tag, c.UserID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "admin_device_preferred_exit_set",
		"target_user_id="+itoa64(userID)+" hostname="+hostname+" tag="+tag)
	res := acl.ApplyACLPipelineForPlane(a.DB, a.HSForUser(userID), "", nil, c.Username,
		"admin_device_preferred_exit_set target_user_id="+itoa64(userID)+" hostname="+hostname+" tag="+tag, a.Cfg.ACLWithViaEnabled)
	if !res.Applied {
		http.Redirect(w, r, "/admin/devices?err="+errToQuery(res.Err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/devices?ok=1", http.StatusSeeOther)
}

// callerOwnsDevice returns true iff the caller has a
// node_owner_map row for (user_id, lowercased hostname).
// The check is intentionally strict — a user can only
// set prefs for devices they actually own (per the
// v0.28.0 backfill). Impersonation would let alice
// change bob's device pref via the /my/ endpoint, which
// is exactly what we don't want.
//
// 2026-07-25: v0.28.4.
func (a *App) callerOwnsDevice(r *http.Request, userID int64, lowerHostname string) bool {
	if lowerHostname == "" {
		return false
	}
	// node_owner_map.hostname is stored as the headscale
	// givenName (e.g. "MSI" for the MSI device). The
	// v0.28.0 tag is the lowercased form. We do a
	// case-insensitive match here.
	var n int
	err := a.DB.QueryRow(
		`SELECT COUNT(*) FROM node_owner_map WHERE tagged_by_user_id = ? AND LOWER(hostname) = ?`,
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
