package handlers

// handlers_my_preauth.go — POST /my/preauth: generate a 1h single-use
// preauth key for the current user. The key string is shown once on
// the result page; headscale_preauth_id is persisted so we can later
// map a registering node's preAuthKey.id back to this user.
// Extracted from handlers.go.

import (
	"log"
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
	if err != nil {
		log.Printf("web.my.preauth: GetUserHSByID userID=%d err=%v", c.UserID, err)
		http.Error(w, "no headscale user linked", 400)
		return
	}
	if !hsUserID.Valid {
		log.Printf("web.my.preauth: no headscale_user_id for userID=%d username=%q", c.UserID, c.Username)
		http.Error(w, "no headscale user linked", 400)
		return
	}
	log.Printf("web.my.preauth: userID=%d hsUserID=%d, calling CreatePreauthKey", c.UserID, hsUserID.Int64)
	key, err := a.HSForUser(c.UserID).CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		log.Printf("web.my.preauth: CreatePreauthKey hsUserID=%d err=%v", hsUserID.Int64, err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("web.my.preauth: got key from HS, prefix=%q, calling InsertPreauthKey", key.Key[:min(20, len(key.Key))])
	// Save headscale_preauth_id so we can later map a node's preAuthKey
	// back to this portal user when the device registers with this key.
	// 2026-07-11: Этап 10 part 3 — INSERT moved to db.InsertPreauthKey
	if _, err := db.InsertPreauthKey(a.DB, c.UserID, key.Key, time.Now().Add(time.Hour).Unix(), key.ID); err != nil {
		log.Printf("web.my.preauth: InsertPreauthKey userID=%d err=%v", c.UserID, err)
	}
	if err := db.AppendAuditLog(a.DB, c.UserID, c.Username, "preauth_issued", "1h single-use"); err != nil {
		log.Printf("web.my.preauth: AppendAuditLog userID=%d err=%v", c.UserID, err)
	}
	log.Printf("web.my.preauth: success userID=%d, rendering result page", c.UserID)
	a.renderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":     key.Key,
		"Expires": "1 hour",
		"OS":      r.FormValue("os"),
	})
}
