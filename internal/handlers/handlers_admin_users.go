package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
)


// handlers_admin_users.go — extracted from handlers.go.
// Admin user management: GetAdminUsers, PostAdminUser, extractIDFromPath,
// PostAdminDeleteUser. Kept separate because they share admin-user
// concerns distinct from the rest of the file (devices, nodes, DERP, etc).

// ---------- ADMIN ----------

func (a *App) GetAdminUsers(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	rows, err := a.DB.Query(`SELECT id, username, is_admin, headscale_user_id, created_at, theme FROM portal_users ORDER BY id`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var users []db.User
	for rows.Next() {
		var u db.User
		var adminI int
		var createdI int64
		var hsID sql.NullInt64
		var theme sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &adminI, &hsID, &createdI, &theme); err == nil {
			u.IsAdmin = adminI == 1
			u.HeadscaleUserID = hsID.Int64
			u.CreatedAt = time.Unix(createdI, 0)
			if theme.Valid {
				u.Theme = theme.String
			}
			u.PasswordHash = ""
			users = append(users, u)
		}
	}

	// Fetch headscale users and detect orphans (in headscale but not in skygate)
	hsUsers, _ := a.HS.ListUsers()
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

	a.renderWithLayout(w, r, "admin/users.html", c, map[string]any{
		"Users":     users,
		"HSOrphans": orphans,
	})
}

func (a *App) PostAdminUser(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	var existingID int64
	err := a.DB.QueryRow(`SELECT id FROM portal_users WHERE username=?`, username).Scan(&existingID)
	if err == nil {
		http.Error(w, fmt.Sprintf("user %q already exists in skygate", username), 409)
		return
	}
	hsUser, err := a.HS.CreateUser(username)
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
	_, err = a.DB.Exec(`INSERT INTO portal_users(username, password_hash, is_admin, headscale_user_id) VALUES(?,?,?,?)`,
		username, hash, isAdmin, hsID)
	if err != nil {
		http.Error(w, "portal insert: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "user_create", fmt.Sprintf("%s hs_id=%d admin=%v", username, hsID, isAdmin))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

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

func (a *App) PostAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	var username string
	var hsID sql.NullInt64
	err := a.DB.QueryRow(`SELECT username, headscale_user_id FROM portal_users WHERE id=?`, id).
		Scan(&username, &hsID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	hsDeleteMsg := ""
	if hsID.Valid && hsID.Int64 > 0 {
		if err := a.HS.DeleteUser(hsID.Int64); err != nil {
			hsDeleteMsg = fmt.Sprintf(" [headscale: %v]", err)
		} else {
			hsDeleteMsg = " [headscale: deleted]"
		}
	}
	_, _ = a.DB.Exec(`DELETE FROM preauth_keys WHERE user_id=?`, id)
	_, _ = a.DB.Exec(`DELETE FROM audit_log WHERE user_id=?`, id)
	_, err = a.DB.Exec(`DELETE FROM portal_users WHERE id=?`, id)
	if err != nil {
		http.Error(w, "delete: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "user_delete", fmt.Sprintf("id=%d %s hs_id=%d%s", id, username, hsID.Int64, hsDeleteMsg))
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

// PostAdminUserResetPassword updates the password hash for an existing user.
// Used by the per-row reset form on /admin/users.
func (a *App) PostAdminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	var username string
	err := a.DB.QueryRow(`SELECT username FROM portal_users WHERE id=?`, id).Scan(&username)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := a.DB.Exec(`UPDATE portal_users SET password_hash=? WHERE id=?`, hash, id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "user_password_reset", fmt.Sprintf("id=%d %s", id, username))
	if a.Notifier != nil {
		go a.Notifier.SendTelegram(fmt.Sprintf("🔑 Password reset by %s\nuser: %s (id=%d)", c.Username, username, id))
	}
	http.Redirect(w, r, "/admin/users?reset=1", http.StatusFound)
}

