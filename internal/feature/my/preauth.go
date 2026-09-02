// Package my — preauth.go owns POST /my/preauth: generate
// a preauth key for the current user.
//
// refactor-v0.30 Phase B step 5a (2026-07-29): moved from
// internal/handlers/handlers_my_preauth.go. The handler
// used to be a method on *App; it now lives on *Service.
// The key string is shown once on the result page;
// headscale_preauth_id is persisted so a later
// registering node's preAuthKey.id can be mapped back to
// this user.
//
// 2026-08-20 (B155, v1.5.0): added custom_ttl_value +
// custom_ttl_unit (number + h/d/w/y) + a reusable checkbox.
// Pre-B155 the form had only an OS picker and the key was
// always 1h + single-use. Post-B155 the operator can pick
// any TTL in the 1h..5y range (or "never" via 0) + make
// the key reusable so the same key can add multiple
// devices.
package my

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// PostMyPreauth issues a preauth key for the current user.
// The key is shown on the result page
// (user/preauth_result.html) and persisted in
// preauth_keys + the headscale-side key ID is captured
// for the v0.12.0+ per-user control plane mapping.
//
// TTL resolution order (B155, v1.5.0):
//  1. custom_ttl_value + custom_ttl_unit — a free-form
//     "number + unit" pair (h/d/w/y) chosen by the user.
//     Min 1h, max 5y. 0 = no expiry (headscale supports
//     this via a 100y TTL — we cap at 5y to bound the
//     audit log + storage volume).
//  2. ttl — the legacy dropdown ("1h" / "1d" / "1w" /
//     "never"). Kept for back-compat with old browser
//     tabs + any pre-B155 curl users.
//  3. Default "1h" if nothing is provided (preserves
//     pre-B155 behaviour for any caller that POSTs the
//     form without filling the TTL fields).
//
// Reusable:
//   - reusable=1 in the form → CreatePreauthKey(user,
//     expiration, true). The same key string can be used
//     to add multiple devices.
//   - reusable=0 (or missing) → single-use (the
//     pre-B155 default).
//
// All authenticated users can issue keys (the result page
// instructs them to run `tailscale up --authkey=<key>`
// on a device that should join their own tailnet).
func (s *Service) PostMyPreauth(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserHSByID
	hsUserID, _, err := db.GetUserHSByID(s.dbc(), c.UserID)
	if err != nil {
		log.Printf("web.my.preauth: GetUserHSByID userID=%d err=%v", c.UserID, err)
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}
	if !hsUserID.Valid {
		log.Printf("web.my.preauth: no headscale_user_id for userID=%d username=%q", c.UserID, c.Username)
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}

	// B155: TTL resolution.
	expirationStr, ttlSeconds, ttlUsed := resolvePreauthTTL(r)
	if ttlSeconds <= 0 {
		// 0 means "never" in our convention; headscale
		// expects an explicit duration string. Map 0 to
		// 5y (the cap) so the audit + UI still show a
		// finite value. Operators who want literally-
		// forever can use 5y in custom TTL or pick
		// "never" from the dropdown.
		expirationStr = "43800h" // 5y
		ttlSeconds = 5 * 365 * 24 * 3600
	}

	// B155: reusable checkbox. Default false (the
	// pre-B155 single-use behaviour).
	reusable := r.FormValue("reusable") == "1"

	log.Printf("web.my.preauth: userID=%d hsUserID=%d ttl=%s reusable=%v, calling CreatePreauthKey",
		c.UserID, hsUserID.Int64, expirationStr, reusable)
	key, err := s.Backend.HSForUserFn(c.UserID).CreatePreauthKey(hsUserID.Int64, expirationStr, reusable)
	if err != nil {
		log.Printf("web.my.preauth: CreatePreauthKey hsUserID=%d err=%v", hsUserID.Int64, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// B160.2 (2026-08-20): invalidate the headscale
	// node-list cache so the next /my/devices load
	// (which backfills node_owner_map from the new
	// preauth key) gets fresh data instead of
	// hitting the 5s cache. Operator 2026-08-20
	// reported "after issuing a new preauth key +
	// reconnecting a device, the /my/devices
	// table didn't update" — the root cause was
	// the cache showing the pre-issue state.
	s.Backend.HSForUserFn(c.UserID).InvalidateCache()
	keyPrefix := key.Key
	if len(keyPrefix) > 20 {
		keyPrefix = keyPrefix[:20]
	}
	log.Printf("web.my.preauth: got key from HS, prefix=%q, calling InsertPreauthKey", keyPrefix)
	// Save headscale_preauth_id so we can later map a node's preAuthKey
	// back to this portal user when the device registers with this key.
	// 2026-07-11: Этап 10 part 3 — INSERT moved to db.InsertPreauthKey
	// 2026-08-20: B156 — InsertPreauthKey now also writes
	// notified_at=0 (the column added in V058PG). The B156
	// scheduler dedup-via-notified_at would otherwise skip
	// the new key on its first tick (the user just got a key
	// that's "already notified"). A fresh insert with
	// notified_at=0 is the right default.
	now := time.Now()
	if _, err := db.InsertPreauthKey(s.dbc(), c.UserID, key.Key, now.Add(time.Duration(ttlSeconds)*time.Second).Unix(), key.ID); err != nil {
		log.Printf("web.my.preauth: InsertPreauthKey userID=%d err=%v", c.UserID, err)
	}
	detail := fmt.Sprintf("ttl=%s reusable=%v resolved=%s", ttlUsed, reusable, expirationStr)
	if err := db.AppendAuditLog(s.dbc(), c.UserID, c.Username, "preauth_issued", detail); err != nil {
		log.Printf("web.my.preauth: AppendAuditLog userID=%d err=%v", c.UserID, err)
	}
	log.Printf("web.my.preauth: success userID=%d, rendering result page", c.UserID)

	// B155: if the URL has from/to (came from
	// /my/keys/{id}/reissue → this POST), forward to
	// the template so the result page can show
	// "this key replaces key #N".
	var reissueFrom, reissueTo int64
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if v, perr := strconv.ParseInt(fromStr, 10, 64); perr == nil {
			reissueFrom = v
		}
	}
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		if v, perr := strconv.ParseInt(toStr, 10, 64); perr == nil {
			reissueTo = v
		}
	}

	s.Backend.RenderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":         key.Key,
		"Expires":     humanizeTTL(ttlSeconds),
		"OS":          r.FormValue("os"),
		"ReissueFrom": reissueFrom,
		"ReissueTo":   reissueTo,
	})
}

// humanizeTTL formats a TTL in seconds as a human
// string ("1h", "1d", "1w", "1y"). Used by the
// preauth_result page to render the "Expires" field.
func humanizeTTL(seconds int64) string {
	if seconds <= 0 {
		return "never"
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds < 7*86400 {
		return fmt.Sprintf("%dd", seconds/86400)
	}
	if seconds < 365*86400 {
		return fmt.Sprintf("%dw", seconds/(7*86400))
	}
	return fmt.Sprintf("%dy", seconds/(365*86400))
}

// resolvePreauthTTL returns the (expirationStr, ttlSeconds,
// ttlSource) triple from the request. Mirrors the
// B153 custom_ttl_value + custom_ttl_unit logic for
// personal API tokens. On invalid input we fall through
// to the legacy dropdown, then to the 1h default.
func resolvePreauthTTL(r *http.Request) (string, int64, string) {
	// 1. Custom TTL (B155).
	if rawVal := strings.TrimSpace(r.FormValue("custom_ttl_value")); rawVal != "" {
		v, perr := strconv.ParseInt(rawVal, 10, 64)
		if perr == nil && v >= 0 {
			unit := r.FormValue("custom_ttl_unit")
			if unit == "" {
				unit = "d"
			}
			var hours int64
			switch unit {
			case "h":
				hours = v
			case "d":
				hours = v * 24
			case "w":
				hours = v * 24 * 7
			case "y":
				hours = v * 24 * 365
			default:
				hours = v * 24
				unit = "d"
			}
			if v == 0 {
				return "never", 0, "never"
			}
			if hours < 1 {
				// too small → fall through
			} else if hours > 5*24*365 {
				// too large → fall through
			} else {
				return fmt.Sprintf("%dh", hours), hours * 3600, fmt.Sprintf("custom:%d%s", v, unit)
			}
		}
	}
	// 2. Legacy dropdown.
	switch r.FormValue("ttl") {
	case "1h":
		return "1h", 3600, "1h"
	case "1d":
		return "24h", 86400, "1d"
	case "1w":
		return "168h", 7 * 86400, "1w"
	case "never":
		return "never", 0, "never"
	}
	// 3. Default: 1h (preserves pre-B155 behaviour).
	return "1h", 3600, "default-1h"
}
