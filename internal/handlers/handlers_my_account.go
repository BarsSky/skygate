// handlers_my_account.go — self-service account management.
//
// Allows users (non-admin too) to change their own password.
// Admin can also do this from /admin/users, but going through /my/account
// lets users rotate their password without involving an admin.
//
// - GetMyAccount         (GET /my/account, renders the form)
// - PostMyAccountPassword (POST /my/account/password, updates the hash)
//
// Password policy:
// - new_password length >= 8 (matches bcrypt minimum sensible value)
// - confirm == new (form-level double-check)
// - current must match existing hash (defends against session hijacking)
package handlers

import (
	"net/http"

	"skygate/internal/auth"
)

// GetMyAccount renders the account page with a password-change form.
func (a *App) GetMyAccount(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, r, "user/account.html", c, map[string]any{
		"Page":        "account",
		"Title":       "Account",
		"FlashOK":     r.URL.Query().Get("saved"),
		"FlashError":  r.URL.Query().Get("err"),
	})
}

// PostMyAccountPassword validates the three fields and writes a new hash.
// On success redirects back to /my/account?saved=ok; on failure sets ?err=...
func (a *App) PostMyAccountPassword(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/account?err=bad_form", http.StatusFound)
		return
	}
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_new_password")

	if current == "" || next == "" || confirm == "" {
		http.Redirect(w, r, "/my/account?err=fields_empty", http.StatusFound)
		return
	}
	if next != confirm {
		http.Redirect(w, r, "/my/account?err=passwords_dont_match", http.StatusFound)
		return
	}
	if len(next) < 8 {
		http.Redirect(w, r, "/my/account?err=password_too_short", http.StatusFound)
		return
	}

	// Verify current password against stored hash
	var hash string
	err := a.DB.QueryRow(`SELECT password_hash FROM portal_users WHERE id=?`, c.UserID).Scan(&hash)
	if err != nil {
		http.Redirect(w, r, "/my/account?err=user_not_found", http.StatusFound)
		return
	}
	if !auth.CheckPassword(hash, current) {
		a.audit(c.UserID, c.Username, "password_change_fail", "wrong current")
		http.Redirect(w, r, "/my/account?err=wrong_current_password", http.StatusFound)
		return
	}

	// Hash new password (bcrypt cost 12, matches auth.HashPassword)
	newHash, err := auth.HashPassword(next)
	if err != nil {
		http.Redirect(w, r, "/my/account?err=hash_failed", http.StatusFound)
		return
	}
	if _, err := a.DB.Exec(`UPDATE portal_users SET password_hash=? WHERE id=?`, newHash, c.UserID); err != nil {
		http.Redirect(w, r, "/my/account?err=db_error", http.StatusFound)
		return
	}

	a.audit(c.UserID, c.Username, "password_change", "")
	http.Redirect(w, r, "/my/account?saved=ok", http.StatusFound)
}
