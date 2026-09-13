package admin

// users.go — admin user CRUD (/admin/users).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/handlers_admin_users.go.
//
// Handlers: GetAdminUsers, PostAdminUser, PostAdminDeleteUser,
// PostAdminHSOrphanAdopt, PostAdminUserResetPassword,
// PostAdminUserRename. Helper: extractIDFromPath (also used by
// devices.go for /admin/nodes/{id}/tag|untag).

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
	"skygate/internal/headscale"
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

	// 2026-09-12 (v1.5.2 admin-user-sync T6): detect SKYGATE_ADMIN_USER
	// drift and render a banner with the appropriate remediation
	// button. Banner only shows when the pure decision function
	// returns a non-None mode — see users_sync_banner.go for the
	// cases. Failures (DB / headscale down) hide the banner — never
	// show an erroneous nag.
	expectedAdmin := ""
	if s.Cfg != nil {
		expectedAdmin = s.Cfg.BootstrapAdminUser
	}
	syncMode, syncFacts := s.adminUserSyncBanner(r.Context(), expectedAdmin)

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
		//
		// 2026-09-12 (v1.5.2 admin-user-sync T4): added
		// FlashRenamed for the ?renamed=<old_username> param that
		// PostAdminUserRename emits on success.
		//
		// 2026-09-12 (v1.5.2 admin-user-sync T6): added
		// AdminSyncMode + AdminSyncFacts so the template can
		// render the appropriate "Adopt as Admin" / "Promote"
		// banner when SKYGATE_ADMIN_USER drift is detected.
		"FlashSuccess":       r.URL.Query().Get("ok"),
		"FlashError":         r.URL.Query().Get("err"),
		"FlashHSOrphanAdopt": r.URL.Query().Get("adopted"),
		"FlashHSOrphanExists": r.URL.Query().Get("already_adopted"),
		"FlashRenamed":       r.URL.Query().Get("renamed"),
		"AdminSyncMode":      syncMode.String(),
		"AdminSyncFacts":     syncFacts,
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

// PostAdminUserPromote is the v1.5.2 admin-user-sync T6.1
// "Promote" button INSIDE the AdminSyncPromoteToAdmin banner on
// /admin/users (rendered when a portal row exists with the right
// username + linked HS but is_admin somehow flipped to 0).
//
// Pre-T6.1 the banner just explained the drift — the operator had
// to open the per-row edit form and click "Promote to admin"
// there. T6.1 collapses this into a single click on the drift
// banner itself, mirroring T5's "Adopt as Admin" form inside the
// adopt banner.
//
// Flow:
//   1. Parse {id} from path (the portal_users row to promote).
//   2. Admin-only check. Non-admin → 403.
//   3. Fetch the row's current is_admin + username via
//      GetUserNameAndHSByID. Missing row → 400 (shouldn't happen
//      in practice — the banner only renders for known users).
//   4. Idempotency: if is_admin is already 1, redirect with
//      already_admin=<username> flash (no DB change, no audit row).
//   5. SetPortalUserIsAdmin(1) — single UPDATE.
//   6. Audit row: action='admin_promote', detail includes the
//      username + the operator's claims.Username so the log shows
//      "who clicked Promote" for the post-mortem.
//
// Wire-up: POST /admin/users/{id}/promote in cmd/skygate/main.go.
func (s *Service) PostAdminUserPromote(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	username, _, err := db.GetUserNameAndHSByID(s.dbc(), id)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	// Read current is_admin to detect the no-op case.
	var isAdmin int
	if err := s.dbc().QueryRow(`SELECT is_admin FROM portal_users WHERE id = $1`, id).Scan(&isAdmin); err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if isAdmin == 1 {
		// Already admin — idempotent UX (the banner doesn't show
		// in this case, but a stale reload + click race could).
		http.Redirect(w, r, "/admin/users?already_admin="+url.QueryEscape(username), http.StatusSeeOther)
		return
	}
	if _, err := db.SetPortalUserIsAdmin(s.dbc(), id, true); err != nil {
		http.Error(w, "promote failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "admin_promote",
		fmt.Sprintf("user=%q promoted to admin (drift fix)", username))
	http.Redirect(w, r, "/admin/users?ok="+url.QueryEscape("promoted "+username+" to admin"), http.StatusSeeOther)
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
//      bcrypt-hashed password + headscale_user_id.
//
//      is_admin behavior:
//        - promote_to_admin form field absent or != "true" →
//          InsertPortalUserAdopt (is_admin=0; the B141 default).
//        - promote_to_admin == "true" → InsertPortalUserAdoptAdmin
//          (is_admin=1; added in v1.5.2 admin-user-sync T5,
//          2026-09-12). This is what the startup detection
//          banner (T6) calls when SKYGATE_ADMIN_USER drift is
//          detected.
//
//      ON CONFLICT(username) DO NOTHING closes the concurrent
//      adopt race (atomic primitive; see portal_users.go:
//      InsertPortalUserAdopt).
//   5. Audit log "hs_orphan_adopt" with username + hs_id +
//      outcome + is_admin.
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
	// T5: promote_to_admin flag. The startup-detection banner
	// (T6) sets this when SKYGATE_ADMIN_USER drift is detected;
	// the regular per-row button does NOT set it (preserves B141
	// behaviour). Only an explicit "true" string promotes — any
	// other value (empty, "on", "1", "yes") falls through to
	// is_admin=0 so a future checkbox-style UI doesn't
	// accidentally promote users.
	promote := strings.TrimSpace(r.FormValue("promote_to_admin")) == "true"

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

	// T5: dispatch on promote_to_admin.
	var newID int64
	var inserted bool
	if promote {
		newID, inserted, err = db.InsertPortalUserAdoptAdmin(s.dbc(), hsName, hash, true, hsID)
	} else {
		newID, inserted, err = db.InsertPortalUserAdopt(s.dbc(), hsName, hash, hsID)
	}
	if err != nil {
		http.Error(w, "insert: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !inserted {
		// ON CONFLICT fired — another adopt (or a manual
		// INSERT) already created the row. Don't treat as
		// error; the operator gets a distinct flash so they
		// know it was a no-op. Audit row carries promote_admin
		// so the operator can tell from the log which path
		// they tried.
		s.Backend.Audit(c.UserID, c.Username, "hs_orphan_adopt", fmt.Sprintf("hs_id=%s username=%s outcome=already_adopted id=%d promote_admin=%v", hsIDStr, hsName, newID, promote))
		http.Redirect(w, r, "/admin/users?already_adopted="+url.QueryEscape(hsName), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "hs_orphan_adopt", fmt.Sprintf("hs_id=%s username=%s outcome=inserted id=%d promote_admin=%v", hsIDStr, hsName, newID, promote))
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

// PostAdminUserRename is the v1.5.2 admin-user-sync (option c)
// "Rename" button on /admin/users/{id}. Pre-T4 the operator had
// to SSH into the VM, run `docker exec headscale headscale users
// rename -i <id> <new>`, UPDATE portal_users.username by hand,
// and restart skygate so cached state refreshed — the operator
// reported this as a 3-step + 2-command gap every time
// SKYGATE_ADMIN_USER drifted from the headscale admin user.
//
// T4 wraps that into a single POST:
//   1. Validate the new name against the portal-users pattern
//      (lowercase letters, digits, _ and -; same as PostAdminUser)
//      → 400 BEFORE hitting headscale if invalid.
//   2. No-op short-circuit: if new == current, redirect without
//      calling headscale. Pre-fix this hit headscale every time
//      and produced a stale-cache 500 ("expected exactly one user,
//      found 2" when the rename target matched an existing row).
//   3. If the user has a headscale_user_id linked:
//        a. Call HSGlobalFn().RenameUser(hsID, newUsername). This
//           posts to POST /api/v1/user/{id}/rename/{new_name} (NO
//           body — see internal/headscale/users.go RenameUser).
//        b. If the call returns *APIError with StatusCode=500
//           and body matching "expected exactly one user", redirect
//           with err= explaining the operator must delete the
//           duplicate headscale user first. Don't UPDATE the
//           portal row — we want the rename to be atomic across
//           both systems.
//        c. Any other *APIError → 500 with audit row.
//   4. UPDATE portal_users.username = newUsername (helper:
//      db.UpdatePortalUsername). 0 rows affected → 404.
//   5. Audit log "admin_user_rename" with id + old + new + hs_id.
//   6. 303 redirect to /admin/users?renamed=<old>.
//
// Admin-only. Wire-up is in cmd/skygate/main.go.
func (s *Service) PostAdminUserRename(w http.ResponseWriter, r *http.Request) {
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
	newName := strings.TrimSpace(r.FormValue("new_username"))
	if newName == "" {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("new_username required"), http.StatusSeeOther)
		return
	}
	// Same pattern as PostAdminUser (users.go:118).
	if !regexp.MustCompile(`^[a-z0-9_-]+$`).MatchString(newName) {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("new_username: lowercase letters, digits, _ and - only"), http.StatusSeeOther)
		return
	}

	oldName, hsID, err := db.GetUserNameAndHSByID(s.dbc(), id)
	if errors.Is(err, db.ErrUserNotFound) {
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("user not found"), http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "lookup: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// No-op short-circuit: don't hit headscale if the name
	// didn't change. Pre-T4 this hit headscale every time and
	// surfaced the cache-stale 500 to the operator.
	if newName == oldName {
		s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d %s hs_id=%d outcome=noop", id, oldName, hsID.Int64))
		http.Redirect(w, r, "/admin/users?renamed="+url.QueryEscape(oldName), http.StatusSeeOther)
		return
	}

	hsCall := "ok"
	if hsID.Valid && hsID.Int64 > 0 {
		hsClient := s.HSGlobalFn()
		if hsClient == nil {
			http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("headscale client not available"), http.StatusSeeOther)
			return
		}
		if _, err := hsClient.RenameUser(hsID.Int64, newName); err != nil {
			// Distinguish the headscale-side duplicate-name
			// conflict (500 "expected exactly one user, found
			// N") from generic API errors. Pre-T4 these were
			// both surfaced as opaque 500s; the operator had to
			// SSH into the VM to read headscale logs.
			apiErr := &headscale.APIError{}
			if errors.As(err, &apiErr) && apiErr.StatusCode == 500 && strings.Contains(apiErr.Body, "expected exactly one user") {
				s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d %s hs_id=%d outcome=headscale_duplicate", id, oldName, hsID.Int64))
				http.Redirect(w, r,
					"/admin/users?err="+url.QueryEscape(fmt.Sprintf("headscale already has a user named %q — delete the duplicate first (id=%d)", newName, hsID.Int64)),
					http.StatusSeeOther)
				return
			}
			// Any other APIError / network / generic error →
			// 500 with an audit row so the operator can see
			// the failure context.
			s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d %s hs_id=%d outcome=headscale_err err=%v", id, oldName, hsID.Int64, err))
			http.Error(w, "headscale rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Portal user has no headscale link — we still allow
		// the skygate-side UPDATE so the operator can clean up
		// an orphan row without first having to create the HS
		// counterpart.
		hsCall = "skipped_no_hs_link"
	}

	affected, err := db.UpdatePortalUsername(s.dbc(), id, newName)
	if err != nil {
		// Likely UNIQUE constraint violation (another portal
		// user already has newName). The headscale side has
		// already been renamed; we MUST NOT silently leave
		// them out of sync. Audit and 500.
		s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d %s hs_id=%d outcome=portal_update_err err=%v HEADSCALE_ALREADY_RENAMED", id, oldName, hsID.Int64, err))
		http.Error(w, "portal update: "+err.Error()+fmt.Sprintf(" (HEADSCALE ALREADY RENAMED to %q — revert via `docker exec headscale headscale users rename -i %d %s`)", newName, hsID.Int64, oldName), http.StatusInternalServerError)
		return
	}
	if affected == 0 {
		// Row vanished between GetUserNameAndHSByID and the
		// UPDATE — operator probably clicked Delete in another
		// tab. Don't proceed; the headscale rename already
		// happened, so the next login attempt will create an
		// orphan headscale user that needs cleanup.
		s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d %s hs_id=%d outcome=portal_missing_after_get HEADSCALE_ALREADY_RENAMED", id, oldName, hsID.Int64))
		http.Redirect(w, r, "/admin/users?err="+url.QueryEscape("user vanished mid-rename — headscale already renamed, portal row missing"), http.StatusSeeOther)
		return
	}

	s.Backend.Audit(c.UserID, c.Username, "admin_user_rename", fmt.Sprintf("id=%d old=%s new=%s hs_id=%d outcome=%s", id, oldName, newName, hsID.Int64, hsCall))
	http.Redirect(w, r, "/admin/users?renamed="+url.QueryEscape(oldName), http.StatusSeeOther)
}
