// Package my — preauth.go owns POST /my/preauth: generate
// a 1h single-use preauth key for the current user.
//
// refactor-v0.30 Phase B step 5a (2026-07-29): moved from
// internal/handlers/handlers_my_preauth.go. The handler
// used to be a method on *App; it now lives on *Service.
// The key string is shown once on the result page;
// headscale_preauth_id is persisted so a later
// registering node's preAuthKey.id can be mapped back to
// this user.
package my

import (
	"log"
	"net/http"
	"time"

	"skygate/internal/db"
)

// PostMyPreauth issues a 1h single-use preauth key for the
// current user. The key is shown on the result page
// (user/preauth_result.html) and persisted in
// preauth_keys + the headscale-side key ID is captured
// for the v0.12.0+ per-user control plane mapping.
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
	hsUserID, _, err := db.GetUserHSByID(s.DB, c.UserID)
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
	log.Printf("web.my.preauth: userID=%d hsUserID=%d, calling CreatePreauthKey", c.UserID, hsUserID.Int64)
	key, err := s.Backend.HSForUserFn(c.UserID).CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		log.Printf("web.my.preauth: CreatePreauthKey hsUserID=%d err=%v", hsUserID.Int64, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	keyPrefix := key.Key
	if len(keyPrefix) > 20 {
		keyPrefix = keyPrefix[:20]
	}
	log.Printf("web.my.preauth: got key from HS, prefix=%q, calling InsertPreauthKey", keyPrefix)
	// Save headscale_preauth_id so we can later map a node's preAuthKey
	// back to this portal user when the device registers with this key.
	// 2026-07-11: Этап 10 part 3 — INSERT moved to db.InsertPreauthKey
	if _, err := db.InsertPreauthKey(s.DB, c.UserID, key.Key, time.Now().Add(time.Hour).Unix(), key.ID); err != nil {
		log.Printf("web.my.preauth: InsertPreauthKey userID=%d err=%v", c.UserID, err)
	}
	if err := db.AppendAuditLog(s.DB, c.UserID, c.Username, "preauth_issued", "1h single-use"); err != nil {
		log.Printf("web.my.preauth: AppendAuditLog userID=%d err=%v", c.UserID, err)
	}
	log.Printf("web.my.preauth: success userID=%d, rendering result page", c.UserID)
	s.Backend.RenderWithLayout(w, r, "user/preauth_result.html", c, map[string]any{
		"Key":     key.Key,
		"Expires": "1 hour",
		"OS":      r.FormValue("os"),
	})
}
