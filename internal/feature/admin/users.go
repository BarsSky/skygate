package admin

// users.go — admin user CRUD (/admin/users).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/handlers_admin_users.go.
//
// Handlers: GetAdminUsers, PostAdminUser, PostAdminDeleteUser,
// PostAdminUserResetPassword. Helper: extractIDFromPath (also
// used by devices.go for /admin/nodes/{id}/tag|untag).

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/subnet"
)

// extractIDFromPath pulls the user/node ID segment out of
// /admin/users/{id}/... or /admin/nodes/{id}/... URLs.
// Returns "" for any other path shape.
func extractIDFromPath(path string) string {
	// Supports:
	//   /admin/users/123/delete -> "123"
	//   /admin/nodes/123/untag  -> "123"
	//   /admin/nodes/123/tag    -> "123"
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[1] == "admin" {
		switch parts[2] {
		case "users", "nodes":
			return parts[3]
		}
	}
	return ""
}

// GetAdminUsers renders the /admin/users page (list of portal
// users + headscale orphans). Admin-only.
func (s *Service) GetAdminUsers(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	users, err := db.GetAllPortalUsers(s.dbc())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch headscale users and detect orphans (in headscale but not in skygate)
	hsUsers, _ := s.HSGlobalFn().ListUsers()
	linked := make(map[string]bool)
	for _, u := range users {
		if u.HeadscaleUserID > 0 {
			linked[strconv.FormatInt(u.HeadscaleUserID, 10)] = true
		}
	}
	var orphans []map[string]any
	for _, h := range hsUsers {
		if !linked[h.ID] {
			orphans = append(orphans, map[string]any{
				"HeadscaleID": h.ID,
				"Username":    h.Name,
				"CreatedAt":   h.CreatedAt,
			})
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/users.html", c, map[string]any{
		"Users":     users,
		"HSOrphans": orphans,
		// 2026-08-18 (v1.4.0 B141): flash banner. The pre-B141
		// GetAdminUsers didn't populate FlashSuccess/FlashError,
		// so the ?ok= / ?err= query params that the sibling
		// handlers (PostAdminUser, PostAdminDeleteUser) emit were
		// silently dropped on redirect. B141 adds the same flash
		// pattern that /admin/exit-nodes uses (and reads the new
		// ?adopted= and ?already_adopted= query params emitted by
		// PostAdminHSOrphanAdopt). Back-compat: the existing
		// redirects continue to work (the template renders the
		// banners only when the param is non-empty).
		"FlashSuccess":       r.URL.Query().Get("ok"),
		"FlashError":         r.URL.Query().Get("err"),
		"FlashHSOrphanAdopt": r.URL.Query().Get("adopted"),
		"FlashHSOrphanExists": r.URL.Query().Get("already_adopted"),
	})
}

// PostAdminUser creates a new portal user + matching headscale
// user. Auto-allocates a per-user subnet if Cfg.AutoAllocateSubnetOnUserCreate
// is true (the v0.20.0 default). Admin-only.
func (s *Service) PostAdminUser(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"
	if username == "" || password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}
	if len(password) < 6 {
		http.Error(w, "password too short (min 6)", http.StatusBadRequest)
		return
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(username) {
		http.Error(w, "username: lowercase letters, digits, _ and - only", http.StatusBadRequest)
		return
	}
	_, err := db.GetUserIDByName(s.dbc(), username)
	if err == nil {
		http.Error(w, fmt.Sprintf("user %q already exists in skygate", username), http.StatusConflict)
		return
	}
	if !errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hsUser, err := s.HSGlobalFn().CreateUser(username)
	if err != nil {
		http.Error(w, "headscale create user: "+err.Error(), http.StatusInternalServerError)
		return
	}
	hsID, _ := strconv.ParseInt(hsUser.ID, 10, 64)
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newUserID, err := db.InsertPortalUser(s.dbc(), username, hash, isAdmin, hsID)
	if err != nil {
		http.Error(w, "portal insert: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// 2026-07-20: v0.20.0 — auto-allocate subnet on user
	// create (best-effort, doesn't roll back the user).
	if s.Cfg != nil && s.Cfg.AutoAllocateSubnetOnUserCreate {
		if _, allocErr := subnet.Create(s.dbc(), newUserID, "", ""); allocErr != nil {
			log.Printf("user_create: auto-allocate subnet for %s (id=%d) failed: %v", username, newUserID, allocErr)
			s.Backend.Audit(c.UserID, c.Username, "user_create", fmt.Sprintf("%s hs_id=%d admin=%v auto_allocate=FAIL: %v", username, hsID, isAdmin, allocErr))
		} else {
			s.Backend.Audit(c.UserID, c.Username, "user_create", fmt.Sprintf("%s hs_id=%d admin=%v auto_allocate=ok", username, hsID, isAdmin))
		}
	} else {
		s.Backend.Audit(c.UserID, c.Username, "user_create", fmt.Sprintf("%s hs_id=%d admin=%v", username, hsID, isAdmin))
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// PostAdminDeleteUser deletes a portal user + cascades to headscale,
// preauth keys, audit log, and personal API tokens. Admin-only.
// The user can't delete themselves.
func (s *Service) PostAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == c.UserID {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	username, hsID, err := db.GetUserNameAndHSByID(s.dbc(), id)
	if errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hsDeleteMsg := ""
	if hsID.Valid && hsID.Int64 > 0 {
		if err := s.HSGlobalFn().DeleteUser(hsID.Int64); err != nil {
			hsDeleteMsg = fmt.Sprintf(" [headscale: %v]", err)
		} else {
			hsDeleteMsg = " [headscale: deleted]"
		}
	}
	keysDeleted, _ := db.DeletePreauthKeysByUserID(s.dbc(), int64(id))
	_ = db.DeleteAuditLogByUserID(s.dbc(), int64(id))
	tokensDeleted, _ := db.DeleteAPITokensByUserID(s.dbc(), int64(id))
	_, err = db.DeletePortalUserByID(s.dbc(), id)
	if err != nil {
		http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "user_delete",
		fmt.Sprintf("id=%d %s hs_id=%d%s keys=%d tokens=%d", id, username, hsID.Int64, hsDeleteMsg, keysDeleted, tokensDeleted))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// PostAdminHSOrphanAdopt is the v1.4.0 B141 "Adopt as skygate
// user" button on the /admin/users HSOrphans list. The pre-B141
// UI only DISPLAYED the orphans list (users.go:79, rendered by
// admin/users.html:62-88) — to adopt one the operator had to run
// a manual SQL INSERT into portal_users with the headscale_user_id,
// plus a separate API call to set the password. B141 wraps that
// into a single button per orphan row.
//
// Flow:
//   1. Parse hs_id from form (the headscale user id from the
//      orphans table). 404 if missing.
//   2. Validate password length (>= 6 chars; same rule as
//      PostAdminUser at users.go:99-101).
//   3. Fetch the headscale user by id via HSGlobalFn().ListUsers
//      and find the matching one. 404 if not found (the headscale
//      side was the source of the orphans list, so the id is
//      normally valid — but the operator could click an old
//      bookmarked page after the orphan was deleted).
//   4. INSERT into portal_users with the headscale username +
//      bcrypt-hashed password + headscale_user_id, is_admin=0.
//      ON CONFLICT(username) DO NOTHING closes the concurrent
//      adopt race (atomic primitive; see portal_users.go:
//      InsertPortalUserAdopt).
//   5. Audit log "hs_orphan_adopt" with username + hs_id + outcome.
//   6. 303 redirect to /admin/users?adopted=<username> on success
//      or ?err=... on failure (no-op duplicate gets a separate
//      ?already_adopted=<username> flash so the operator can
//      distinguish "I clicked twice" from "real error").
//
// Admin-only. Wire-up is in cmd/skygate/main.go.
func (s *Service) PostAdminHSOrphanAdopt(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hsIDStr := strings.TrimSpace(r.FormValue("hs_id"))
	if hsIDStr == "" {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("hs_id required"), http.StatusSeeOther)
		return
	}
	password := r.FormValue("password")
	if len(password) < 6 {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("password too short (min 6)"), http.StatusSeeOther)
		return
	}
	// Fetch the headscale user by id. The orphan list is built
	// from ListUsers(), so the id is normally valid — but we
	// re-validate here so a stale form (orphan deleted between
	// page load and submit) gets a clean 404 instead of an
	// INSERT that creates a row with a non-existent hsID.
	hsUsers, _ := s.HSGlobalFn().ListUsers()
	var hsName string
	var hsID int64
	found := false
	for _, h := range hsUsers {
		if h.ID == hsIDStr {
			hsName = h.Name
			hsID, _ = strconv.ParseInt(h.ID, 10, 64)
			found = true
			break
		}
	}
	if !found {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("headscale user not found: "+hsIDStr), http.StatusSeeOther)
		return
	}
	if err := validateHSOrphanName(hsName); err != nil {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, "hash: "+err.Error(), http.StatusInternalServerError)
		return
	}
	newID, inserted, err := db.InsertPortalUserAdopt(s.dbc(), hsName, hash, hsID)
	if err != nil {
		http.Error(w, "insert: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !inserted {
		// ON CONFLICT fired — another adopt (or a manual
		// INSERT) already created the row. Don't treat as
		// error; the operator gets a distinct flash so they
		// know it was a no-op.
		s.Backend.Audit(c.UserID, c.Username, "hs_orphan_adopt", fmt.Sprintf("hs_id=%s username=%s outcome=already_adopted id=%d", hsIDStr, hsName, newID))
		http.Redirect(w, r, "/admin/users?already_adopted="+url.QueryEscape(hsName), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "hs_orphan_adopt", fmt.Sprintf("hs_id=%s username=%s outcome=inserted id=%d", hsIDStr, hsName, newID))
	http.Redirect(w, r, "/admin/users?adopted="+url.QueryEscape(hsName), http.StatusSeeOther)
}

// validateHSOrphanName checks the headscale username against
// the skygate username pattern (lowercase letters, digits, _
// and -). The pre-B141 SQL-INSERT path didn't enforce this
// (it just stuffed whatever the headscale side had into
// portal_users.username), and headscale allows names that
// skygate doesn't (e.g. dots in some configs). B141 enforces
// the same pattern that PostAdminUser uses (users.go:103-106),
// so the two create paths produce identical rows.
//
// Extracted from PostAdminHSOrphanAdopt so the rule is unit-
// testable without a DB / headscale. Returns nil for valid
// names; a descriptive error otherwise.
func validateHSOrphanName(name string) error {
	if name == "" {
		return fmt.Errorf("headscale username is empty")
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(name) {
		return fmt.Errorf("headscale username %q doesn't match skygate pattern (lowercase letters, digits, _ and - only)", name)
	}
	return nil
}

// PostAdminUserResetPassword resets a user's password to a
// new value supplied by the admin. Sends a Telegram alert
// (if Notifier is configured). Admin-only.
func (s *Service) PostAdminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	newPassword := r.FormValue("new_password")
	if len(newPassword) < 6 {
		http.Error(w, "password too short (min 6)", http.StatusBadRequest)
		return
	}
	username, err := db.GetUserNameByID(s.dbc(), id)
	if errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := db.UpdatePasswordHash(s.dbc(), id, hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "user_password_reset", fmt.Sprintf("id=%d %s", id, username))
	if s.Notifier != nil {
		go s.Notifier.SendAlert(fmt.Sprintf("🔑 Password reset by %s\nuser: %s (id=%d)", c.Username, username, id))
	}
	http.Redirect(w, r, "/admin/users?reset=1", http.StatusFound)
}
