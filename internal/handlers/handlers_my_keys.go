package handlers

// handlers_my_keys.go — /my/keys self-service: list preauth keys the
// user has been issued, and expire unused ones.
// Extracted from handlers.go.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"skygate/internal/db"
)

// GetMyKeys lists every preauth key the current user has been issued,
// with its lifecycle state. Lets a user see what's outstanding and
// revoke keys that are no longer needed (e.g. they generated a key
// for a one-off install, did the install, and don't want the unused
// key to sit around).
func (a *App) GetMyKeys(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.ListPreauthKeysByUser
	// Returns []db.PreauthKey, which the template iterates over the
	// same fields the old local keyRow did (ID, Key, Used, ExpiresAt,
	// CreatedAt, HeadscalePreauthID). We rebind the slice into a
	// []any for the template rather than introducing a template-
	// specific wrapper struct.
	rows, err := db.ListPreauthKeysByUser(a.DB, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Live "used" check: if any headscale node currently has this
	// key as its preAuthKey, mark used even if our local flag is
	// behind. Same logic as countMyPreAuthKeys.
	if hsUsed, hsErr := a.HS.ListAllNodes(); hsErr == nil {
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
	now := time.Now().Unix()
	a.renderWithLayout(w, r, "user/keys.html", c, map[string]any{
		"Keys":    rows,
		"HasKeys": len(rows) > 0,
		"Now":     now,
	})
}

// PostMyKeyExpire revokes a preauth key by ID. The key must belong
// to the current user (we filter on user_id in the SELECT/UPDATE
// chain). Used keys cannot be expired - the action is a no-op for
// them and we redirect back to the list with no error. Already-
// expired keys are also no-ops, idempotently.
//
// Workflow:
//  1. Look up the key by id, scoped to current user.
//  2. If used or already expired: redirect to /my/keys.
//  3. Call headscale.ExpirePreauthKey(userID, keyID).
//  4. On success, mark the local preauth_keys row as expired by
//     setting expires_at to the current time. We do NOT delete
//     the row - it's audit history.
//
// On error from headscale we return 500 with the message; the user
// can retry. We do NOT mutate the local row in that case.
func (a *App) PostMyKeyExpire(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Path parameter: /my/keys/{id}/expire
	idStr := r.PathValue("id")
	if idStr == "" {
		http.Error(w, "missing key id", 400)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad key id", 400)
		return
	}
	// Look up the key, scoped to current user.
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.GetPreauthKeyByID
	k, err := db.GetPreauthKeyByID(a.DB, id, c.UserID)
	if errors.Is(err, db.ErrPreauthKeyNotFound) {
		http.Error(w, "key not found", 404)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// No-ops for used or already-expired keys.
	now := time.Now().Unix()
	if k.Used {
		a.audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already used", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	if k.ExpiresAt > 0 && k.ExpiresAt <= now {
		a.audit(c.UserID, c.Username, "preauth_expire_noop", fmt.Sprintf("key_id=%d already expired", id))
		http.Redirect(w, r, "/my/keys", http.StatusFound)
		return
	}
	// Resolve the headscale user ID for this portal user. We need
	// it for the headscale API/CLI call.
	// 2026-07-11: Этап 10 part 1 — moved to db.GetHSIDByID
	hsUserID, err := db.GetHSIDByID(a.DB, c.UserID)
	if err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", 400)
		return
	}
	// Expire in headscale. The local headscale_preauth_id is the
	// primary identifier; without it we fall back to... nothing,
	// the key is no longer addressable in headscale. (This is the
	// case for the 5/7 michail keys from before the API field
	// started populating. The user-facing behavior is the same:
	// we mark the local row expired and move on. They can't
	// register a device with the key anyway because the underlying
	// key string is in our DB only, not headscale.)
	if k.HeadscalePreauthID != "" {
		if err := a.HS.ExpirePreauthKey(hsUserID.Int64, k.HeadscalePreauthID); err != nil {
			http.Error(w, "headscale expire failed: "+err.Error(), 500)
			return
		}
	}
	// Mark local row as expired. We set expires_at to the current
	// time so the dashboard's 3-way split picks it up immediately
	// on next render (no separate 'expired' column; we reuse the
	// expires_at timestamp convention used for TTL-based expiry).
	// 2026-07-11: Этап 10 part 3 — UPDATE moved to db.ExpirePreauthKey
	if err := db.ExpirePreauthKey(a.DB, id, c.UserID, now); err != nil {
		http.Error(w, "local update failed: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "preauth_expired", fmt.Sprintf("key_id=%d", id))
	http.Redirect(w, r, "/my/keys", http.StatusFound)
}
