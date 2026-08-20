// Package my — keys.go owns the /my/keys self-service
// page (list preauth keys the user has been issued +
// expire + reissue unused ones).
//
// refactor-v0.30 Phase B step 5a (2026-07-29): moved from
// internal/handlers/handlers_my_keys.go. The two
// handlers (GetMyKeys + PostMyKeyExpire) used to be
// methods on *App; they now live on *Service.
//
// 2026-08-20 (B155, v1.5.0): added PostMyKeyReissue + per-row
// ExpiresWarn/ExpiresInWords/Renewable (same UX pattern as
// B153's personal_api_tokens) so users can reissue a
// preauth key with a fresh TTL instead of letting it expire
// and have to start over.
package my

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"skygate/internal/db"
)

// GetMyKeys lists every preauth key the current user has
// been issued, with its lifecycle state. Lets a user
// see what's outstanding and revoke keys that are no
// longer needed (e.g. they generated a key for a
// one-off install, did the install, and don't want the
// unused key to sit around).
//
// B155 (v1.5.0) enrichment: per-row ExpiresWarn /
// ExpiresInWords / Renewable so the template can render
// the same red/yellow pills the /my/tokens page uses
// (B153). ExpiringCount banner trigger when ≥1 unused,
// not-yet-expired key is within 14 days of expiry. Post-
// reissue flash via ?reissued=1&from=<N>&to=<M>. Dedicated
// ?reissue=ID form for power users who want a custom TTL.
func (s *Service) GetMyKeys(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.ListPreauthKeysByUser.
	// Returns []db.PreauthKey, which the template iterates over the
	// same fields the old local keyRow did (ID, Key, Used, ExpiresAt,
	// CreatedAt, HeadscalePreauthID).
	rows, err := db.ListPreauthKeysByUser(s.DB, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Live "used" check: if any headscale node currently has this
	// key as its preAuthKey, mark used even if our local flag is
	// behind. Same logic as countMyPreAuthKeys.
	// 2026-07-15: v0.12.0 — route to the user's own control plane
	// (HSForUser); the key is owned by this user, so the relevant
	// headscale is the one that issued it.
	if hsUsed, hsErr := s.Backend.HSForUserFn(c.UserID).ListAllNodes(); hsErr == nil {
		liveByKeyID := map[string]bool{}
		for _, n := range hsUsed {
			if n.PreAuthKeyID != "" {
				liveByKeyID[n.PreAuthKeyID] = true
			}
		}
		for i := range rows {
			if rows[i].HeadscalePreauthID != "" && liveByKeyID[rows[i].HeadscalePreauthID] {
				rows[i].Used = true
			}
		}
	}

	// B155: per-row warning computation + ExpiringCount
	// banner. Mirrors the B153 token pattern exactly —
	// the warning logic lives in the handler so the
	// template is pure presentation. An unused, not-yet-
	// expired key with expires_at within 14d gets the
	// red "soon" badge. Reissue is only meaningful for
	// such keys (used/expired keys can't be reissued).
	now := time.Now()
	nowUnix := now.Unix()
	const expiringSoonWindow = 14 * 24 * time.Hour
	expiringCount := 0
	viewRows := make([]map[string]any, 0, len(rows))
	for _, k := range rows {
		view := map[string]any{
			"ID":                 k.ID,
			"Key":                k.Key,
			"HeadscalePreauthID": k.HeadscalePreauthID,
			"Used":               k.Used,
			"ExpiresAt":          k.ExpiresAt,
			"CreatedAt":          k.CreatedAt,
			"ExpiresWarn":        "",
			"ExpiresInWords":     "",
			"Renewable":          false,
		}
		// Only compute warnings for unused keys with a
		// future expiry. Used keys (consumed) and never-
		// expiring keys (ExpiresAt==0) get no warning.
		if !k.Used && k.ExpiresAt > 0 {
			delta := time.Unix(k.ExpiresAt, 0).Sub(now)
			days := int(delta / (24 * time.Hour))
			hours := int(delta / time.Hour)
			warn := ""
			inWords := ""
			switch {
			case delta <= 0:
				warn = "expired"
				inWords = s.I18n.T(lang, "keys.expired")
			case hours < 24:
				warn = "soon"
				if hours <= 1 {
					inWords = s.I18n.Tf(lang, "keys.expires_in_hours", 1)
				} else {
					inWords = s.I18n.Tf(lang, "keys.expires_in_hours", hours)
				}
			case days <= 7:
				warn = "soon"
				if days == 1 {
					inWords = s.I18n.T(lang, "keys.expires_tomorrow")
				} else {
					inWords = s.I18n.Tf(lang, "keys.expires_in_days", days)
				}
			case days <= 30:
				warn = "month"
				inWords = s.I18n.Tf(lang, "keys.expires_in_days", days)
			}
			if warn != "" {
				view["ExpiresWarn"] = warn
				view["ExpiresInWords"] = inWords
			}
			// Banner trigger: unused, future expiry,
			// within 14d. The "already expired" keys
			// (warn="expired") also count — the user
			// needs a nudge to revoke them.
			if delta <= expiringSoonWindow {
				expiringCount++
			}
			// Reissue button is meaningful iff the
			// key is unused AND not already expired.
			// Used/expired keys return 404 from the
			// reissue handler, so we don't show the
			// button on rows that would 404.
			view["Renewable"] = delta > 0
		}
		viewRows = append(viewRows, view)
	}

	data := map[string]any{
		"Keys":          viewRows,
		"HasKeys":       len(viewRows) > 0,
		"Now":           nowUnix,
		"ExpiringCount": expiringCount,
	}

	// B155: dedicated reissue form. If ?reissue=ID is
	// in the URL, surface the targeted key's id+prefix
	// as .ReissueForm so the template can render the
	// "Reissue with new TTL" card below the table.
	if reissueIDStr := r.URL.Query().Get("reissue"); reissueIDStr != "" {
		if reissueID, perr := strconv.ParseInt(reissueIDStr, 10, 64); perr == nil {
			for _, k := range viewRows {
				if k["ID"] == reissueID {
					// Build a short prefix for the
					// operator's eyes (matches the
					// "abc...xyz" format in the
					// table). Defensive against
					// very short keys.
					prefix := fmt.Sprintf("%v", k["Key"])
					if len(prefix) > 18 {
						prefix = prefix[:18] + "…"
					}
					data["ReissueForm"] = map[string]any{
						"ID":     reissueID,
						"Prefix": prefix,
					}
					break
				}
			}
		}
	}

	// B155: post-reissue success flash. The handler
	// redirects to /my/keys?reissued=1&from=<N>&to=<M>
	// after a successful reissue; we render an alert
	// with the same "Key #N replaced by #M" copy.
	if r.URL.Query().Get("reissued") == "1" {
		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")
		if from, ferr := strconv.ParseInt(fromStr, 10, 64); ferr == nil {
			if to, terr := strconv.ParseInt(toStr, 10, 64); terr == nil {
				data["reissuedFrom"] = from
				data["reissuedTo"] = to
			}
		}
	}

	s.Backend.RenderWithLayout(w, r, "user/keys.html", c, data)
}

// PostMyKeyReissue reissues a preauth key by ID. The
// workflow (B155, v1.5.0):
//
//  1. Look up the key, scoped to the current user.
//  2. If used: 400 (can't reissue a consumed key — the
//     user must issue a new one from scratch).
//  3. If already expired: 400 (same reasoning).
//  4. Mark the old key as expired in BOTH headscale
//     (via ExpirePreauthKey) and the local row (via
//     db.ExpirePreauthKey) — the row stays for audit
//     history.
//  5. Issue a NEW key in headscale via CreatePreauthKey
//     with the same TTL as the old one (mirrors the
//     pre-B155 user expectation: a "reissue" preserves
//     the key's properties, just refreshes the
//     string + expiry).
//  6. Persist the new key in preauth_keys (the local
//     mirror; headscale_preauth_id captured for the
//     same temporal-backfill match the original key
//     had).
//  7. Audit "preauth_reissued from_id=<old> to_id=<new>".
//  8. Redirect to the result page
//     /my/preauth/result?key=<new>&from=<old>&to=<new>
//     so the user sees the new raw key + the
//     "this key replaces key #N" banner.
//
// If the headscale CreatePreauthKey call fails AFTER
// the local expire, the old key is effectively dead
// but the user has no new key to use. The audit log
// captures the partial state so an operator can
// investigate. The /my/keys page's "Used" check
// reads headscale's reality, so a stuck state is
// visible from the UI.
func (s *Service) PostMyKeyReissue(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	k, err := db.GetPreauthKeyByID(s.DB, id, c.UserID)
	if errors.Is(err, db.ErrPreauthKeyNotFound) {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reject used keys.
	if k.Used {
		s.Backend.Audit(c.UserID, c.Username, "preauth_reissue_noop", fmt.Sprintf("key_id=%d already used", id))
		http.Error(w, "key already used", http.StatusBadRequest)
		return
	}

	// Reject already-expired keys.
	now := time.Now().Unix()
	if k.ExpiresAt > 0 && k.ExpiresAt <= now {
		s.Backend.Audit(c.UserID, c.Username, "preauth_reissue_noop", fmt.Sprintf("key_id=%d already expired", id))
		http.Error(w, "key already expired", http.StatusBadRequest)
		return
	}

	// Resolve the headscale user.
	hsUserID, _, err := db.GetUserHSByID(s.DB, c.UserID)
	if err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}

	// Compute the TTL the OLD key had. We carry it
	// forward to the new key (the pre-B155 user
	// expectation: a "reissue" preserves the
	// properties, just refreshes the string +
	// expiry). If the old key has no recorded expiry
	// (ExpiresAt==0 means "never"), fall back to the
	// standard 1h default.
	ttlSeconds := k.ExpiresAt - k.CreatedAt
	if ttlSeconds <= 0 {
		ttlSeconds = 3600 // 1h fallback
	}
	expirationStr := durationFromSeconds(ttlSeconds)

	// Mark the OLD key as expired in headscale +
	// local. The local row is NOT deleted; it stays
	// around as audit history (same pattern as
	// PostMyKeyExpire).
	if k.HeadscalePreauthID != "" {
		if err := s.Backend.HSForUserFn(c.UserID).ExpirePreauthKey(hsUserID.Int64, k.HeadscalePreauthID); err != nil {
			log.Printf("web.my.reissue: ExpirePreauthKey old id=%s err=%v", k.HeadscalePreauthID, err)
			// Continue anyway — the local expire
			// is what gates the dashboard, and
			// headscale's state will catch up on
			// the next /my/keys load.
		}
	}
	if err := db.ExpirePreauthKey(s.DB, id, c.UserID, now); err != nil {
		http.Error(w, "local expire failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Issue the NEW key in headscale.
	newKey, err := s.Backend.HSForUserFn(c.UserID).CreatePreauthKey(hsUserID.Int64, expirationStr, false)
	if err != nil {
		log.Printf("web.my.reissue: CreatePreauthKey err=%v", err)
		http.Error(w, "headscale create failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist the new key in the local mirror.
	newID, err := db.InsertPreauthKey(s.DB, c.UserID, newKey.Key, now+ttlSeconds, newKey.ID)
	if err != nil {
		log.Printf("web.my.reissue: InsertPreauthKey err=%v", err)
	}

	s.Backend.Audit(c.UserID, c.Username, "preauth_reissued", fmt.Sprintf("from_id=%d to_id=%d ttl=%s", id, newID, expirationStr))

	// Render the preauth_result page directly with the
	// new key (mirrors the PostMyPreauth flow so the
	// key never appears in a URL/log). The result page
	// shows "this key replaces key #N" via the
	// .ReissueFrom + .ReissueTo fields the template
	// reads.
	s.Backend.RenderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":         newKey.Key,
		"Expires":     durationFromSeconds(ttlSeconds),
		"OS":          r.FormValue("os"),
		"ReissueFrom": id,
		"ReissueTo":   newID,
	})
}

// durationFromSeconds converts a TTL in seconds to the
// headscale `CreatePreauthKey` duration string format
// (e.g. "1h", "24h", "168h", "8760h"). Used by
// PostMyKeyReissue to carry the OLD key's TTL forward
// to the NEW key.
//
// We use the hour granularity (the smallest unit
// headscale accepts in its API). For sub-hour TTLs
// the original key's string is something like "1h"
// already; rounding to hours here keeps the new
// key's TTL close to the original.
func durationFromSeconds(seconds int64) string {
	if seconds <= 0 {
		return "1h"
	}
	hours := seconds / 3600
	if hours < 1 {
		hours = 1
	}
	return fmt.Sprintf("%dh", hours)
}

// PostMyKeyExpire revokes a preauth key by ID. The key
// must belong to the current user (we filter on user_id
// in the SELECT/UPDATE chain). Used keys cannot be
// expired — the action is a no-op for them and we
// redirect back to the list with no error. Already-
// expired keys are also no-ops, idempotently.
//
// Workflow:
//  1. Look up the key by id, scoped to current user.
//  2. If used or already expired: redirect to /my/keys.
//  3. Call headscale.ExpirePreauthKey(userID, keyID).
//  4. On success, mark the local preauth_keys row as
//     expired by setting expires_at to the current time.
//     We do NOT delete the row — it's audit history.
//
// On error from headscale we return http.StatusInternalServerError with the
// message; the user can retry. We do NOT mutate the
// local row in that case.
func (s *Service) PostMyKeyExpire(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Path parameter: /my/keys/{id}/expire
	idStr := r.PathValue("id")
	if idStr == "" {
		http.Error(w, "missing key id", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad key id", http.StatusBadRequest)
		return
	}
	// Look up the key, scoped to current user.
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.GetPreauthKeyByID
	k, err := db.GetPreauthKeyByID(s.DB, id, c.UserID)
	if errors.Is(err, db.ErrPreauthKeyNotFound) {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// No-ops for used or already-expired keys.
	now := time.Now().Unix()
	if k.Used {
		s.Backend.Audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already used", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	if k.ExpiresAt > 0 && k.ExpiresAt <= now {
		s.Backend.Audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already expired", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	// Resolve the headscale user ID for this portal user.
	// 2026-07-11: Этап 10 part 1 — moved to db.GetHSIDByID
	hsUserID, err := db.GetHSIDByID(s.DB, c.UserID)
	if err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", http.StatusBadRequest)
		return
	}
	// Expire in headscale. The local headscale_preauth_id is the
	// primary identifier; without it we fall back to... nothing,
	// the key is no longer addressable in headscale. (This is the
	// case for the 5/7 user1 keys from before the API field
	// started populating. The user-facing behavior is the same:
	// we mark the local row expired and move on. They can't
	// register a device with the key anyway because the underlying
	// key string is in our DB only, not headscale.)
	if k.HeadscalePreauthID != "" {
		if err := s.Backend.HSForUserFn(c.UserID).ExpirePreauthKey(hsUserID.Int64, k.HeadscalePreauthID); err != nil {
			http.Error(w, "headscale expire failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Mark local row as expired. We set expires_at to the current
	// time so the dashboard's 3-way split picks it up immediately
	// on next render (no separate 'expired' column; we reuse the
	// expires_at timestamp convention used for TTL-based expiry).
	// 2026-07-11: Этап 10 part 3 — UPDATE moved to db.ExpirePreauthKey
	if err := db.ExpirePreauthKey(s.DB, id, c.UserID, now); err != nil {
		http.Error(w, "local update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "preauth_expired", fmt.Sprintf("key_id=%d", id))
	http.Redirect(w, r, "/my/keys", http.StatusFound)
}
