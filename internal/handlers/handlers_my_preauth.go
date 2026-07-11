package handlers

// handlers_my_preauth.go — POST /my/preauth: generate a 1h single-use
// preauth key for the current user. The key string is shown once on
// the result page; headscale_preauth_id is persisted so we can later
// map a registering node's preAuthKey.id back to this user.
// Extracted from handlers.go.

import (
	"net/http"
	"time"

	"skygate/internal/db"
)

func (a *App) PostMyPreauth(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// 2026-07-11: Этап 10 part 1 — moved to db.GetUserHSByID
	hsUserID, _, err := db.GetUserHSByID(a.DB, c.UserID)
	if err != nil || !hsUserID.Valid {
		http.Error(w, "no headscale user linked", 400)
		return
	}
	key, err := a.HS.CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Save headscale_preauth_id so we can later map a node's preAuthKey
	// back to this portal user when the device registers with this key.
	_, _ = a.DB.Exec(`INSERT INTO preauth_keys(user_id, key, expires_at, headscale_preauth_id) VALUES(?,?,?,?)`,
		c.UserID, key.Key, time.Now().Add(time.Hour).Unix(), key.ID)
	a.audit(c.UserID, c.Username, "preauth_issued", "1h single-use")
	a.renderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":     key.Key,
		"Expires": "1 hour",
		"OS":      r.FormValue("os"),
	})
}
