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
		http.Error(w, "forbidden", 403)
		return
	}
	users, err := db.GetAllPortalUsers(s.DB)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
	})
}

// PostAdminUser creates a new portal user + matching headscale
// user. Auto-allocates a per-user subnet if Cfg.AutoAllocateSubnetOnUserCreate
// is true (the v0.20.0 default). Admin-only.
func (s *Service) PostAdminUser(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"
	if username == "" || password == "" {
		http.Error(w, "username and password required", 400)
		return
	}
	if len(password) < 6 {
		http.Error(w, "password too short (min 6)", 400)
		return
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(username) {
		http.Error(w, "username: lowercase letters, digits, _ and - only", 400)
		return
	}
	_, err := db.GetUserIDByName(s.DB, username)
	if err == nil {
		http.Error(w, fmt.Sprintf("user %q already exists in skygate", username), 409)
		return
	}
	if !errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, err.Error(), 500)
		return
	}
	hsUser, err := s.HSGlobalFn().CreateUser(username)
	if err != nil {
		http.Error(w, "headscale create user: "+err.Error(), 500)
		return
	}
	hsID, _ := strconv.ParseInt(hsUser.ID, 10, 64)
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	newUserID, err := db.InsertPortalUser(s.DB, username, hash, isAdmin, hsID)
	if err != nil {
		http.Error(w, "portal insert: "+err.Error(), 500)
		return
	}
	// 2026-07-20: v0.20.0 — auto-allocate subnet on user
	// create (best-effort, doesn't roll back the user).
	if s.Cfg != nil && s.Cfg.AutoAllocateSubnetOnUserCreate {
		if _, allocErr := subnet.Create(s.DB, newUserID, "", ""); allocErr != nil {
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
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == c.UserID {
		http.Error(w, "cannot delete yourself", 400)
		return
	}
	username, hsID, err := db.GetUserNameAndHSByID(s.DB, id)
	if errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, "user not found", 404)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
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
	keysDeleted, _ := db.DeletePreauthKeysByUserID(s.DB, int64(id))
	_ = db.DeleteAuditLogByUserID(s.DB, int64(id))
	tokensDeleted, _ := db.DeleteAPITokensByUserID(s.DB, int64(id))
	_, err = db.DeletePortalUserByID(s.DB, id)
	if err != nil {
		http.Error(w, "delete: "+err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "user_delete",
		fmt.Sprintf("id=%d %s hs_id=%d%s keys=%d tokens=%d", id, username, hsID.Int64, hsDeleteMsg, keysDeleted, tokensDeleted))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// PostAdminUserResetPassword resets a user's password to a
// new value supplied by the admin. Sends a Telegram alert
// (if Notifier is configured). Admin-only.
func (s *Service) PostAdminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id <= 0 {
		http.Error(w, "bad id", 400)
		return
	}
	newPassword := r.FormValue("new_password")
	if len(newPassword) < 6 {
		http.Error(w, "password too short (min 6)", 400)
		return
	}
	username, err := db.GetUserNameByID(s.DB, id)
	if errors.Is(err, db.ErrUserNotFound) {
		http.Error(w, "user not found", 404)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := db.UpdatePasswordHash(s.DB, id, hash); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "user_password_reset", fmt.Sprintf("id=%d %s", id, username))
	if s.Notifier != nil {
		go s.Notifier.SendAlert(fmt.Sprintf("🔑 Password reset by %s\nuser: %s (id=%d)", c.Username, username, id))
	}
	http.Redirect(w, r, "/admin/users?reset=1", http.StatusFound)
}
