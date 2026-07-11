package db

import (
	"database/sql"
	"errors"
	"time"
)

// portal_users  —  helpers
//
// 2026-07-11: refactor v0.6.0 (Этап 10 part 1). Before this file the
// same SQL strings were duplicated across 9 handler files:
//
//   handlers_auth.go             — login (SELECT id, hash, is_admin)
//   handlers_admin_users.go      — list / create / delete / reset password
//   handlers_dashboard.go        — get hs username
//   handlers_my_devices.go       — get hs user id + username
//   handlers_my_keys.go          — get hs user id (for ExpirePreauthKey)
//   handlers_my_preauth.go       — get hs user id (for CreatePreauthKey)
//   handlers_my_account.go       — change own password
//   handlers_node_ownership.go   — get other users' hs user ids
//   exit_rules.go                — list all usernames (for ACL tagOwners)
//
// Note: GetUserTheme / SetUserTheme in db.go also touch portal_users
// but stay where they are — they're tiny and only used by the theme
// switcher, not by anything in this batch.
//
// The helpers here are split by call-site intent, not by SQL shape:
//   - 3 columns → GetUserCredentials  (login needs the trio atomically)
//   - 2 columns with different order → GetUserNameAndHSByID, GetUserHSByID
//   - 1 column → GetUserNameByID, GetPasswordHashByID, GetHSIDByID, etc.
// Splitting the (name, hsID) pair into two helpers mirrors the two
// existing call shapes (admin delete needs name first, my_devices/my_preauth
// need hsID first) and avoids forcing callers to rename return values.

// ErrUserNotFound is returned by single-row lookups (GetUserByID,
// GetUserNameByID, etc.) when no matching row exists. Callers can use
// errors.Is to detect "no such user" and respond with 404 / redirect.
// The multi-row variants (GetAllPortalUsers, GetPortalUsernames) and
// "find" helpers do NOT return this — they return an empty slice.
var ErrUserNotFound = errors.New("db: portal_user not found")

// GetUserCredentials returns (id, password_hash, is_admin) for the
// user with the given username. Used by PostLogin to authenticate
// without a separate round-trip per column. Returns ErrUserNotFound
// if the username doesn't exist; the caller is expected to treat
// "no such user" and "wrong password" identically (don't leak which
// case happened) — so this helper deliberately returns the typed
// error and the auth handler maps both to the same response.
func GetUserCredentials(d *sql.DB, username string) (int64, string, bool, error) {
	var id int64
	var hash string
	var adminI int
	err := d.QueryRow(qSelectUserByName, username).Scan(&id, &hash, &adminI)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, ErrUserNotFound
	}
	if err != nil {
		return 0, "", false, err
	}
	return id, hash, adminI == 1, nil
}

// GetUserIDByName returns the portal_users.id of a user by name.
// Used by PostAdminUser to detect "username already exists" before
// creating a headscale user. Returns ErrUserNotFound if no match.
func GetUserIDByName(d *sql.DB, username string) (int64, error) {
	var id int64
	err := d.QueryRow(qSelectUserIDByName, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	return id, err
}

// GetUserNameByID returns the username for a given user id. Used by
// PostAdminUserResetPassword (audit-log message) and GetDashboard
// (find this user's headscale username to scope the metrics query).
// Returns ErrUserNotFound if no match.
func GetUserNameByID(d *sql.DB, id int64) (string, error) {
	var name string
	err := d.QueryRow(qSelectUserNameByID, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	return name, err
}

// GetPasswordHashByID returns the bcrypt hash for a given user id.
// Used by PostMyAccount to verify the current password before allowing
// a self-service password change. Returns ErrUserNotFound if the row
// is gone (shouldn't happen for an authenticated request, but the
// check is cheap).
func GetPasswordHashByID(d *sql.DB, id int64) (string, error) {
	var hash string
	err := d.QueryRow(qSelectPasswordHash, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	return hash, err
}

// GetHSIDByID returns the headscale_user_id for a given user id as a
// sql.NullInt64 (NULL → invalid). Used by PostMyExpireKey, which
// needs the hs id to call headscale's API to expire the key.
//
// On "user not found" we return (zero-value, nil) rather than
// ErrUserNotFound because callers treat "user doesn't exist" and
// "user has no hs link" identically — both should short-circuit
// to a 400 "no headscale user linked". A caller that needs to
// distinguish can call GetUserNameByID first.
func GetHSIDByID(d *sql.DB, id int64) (sql.NullInt64, error) {
	var hsID sql.NullInt64
	err := d.QueryRow(qSelectHSIDByID, id).Scan(&hsID)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, nil
	}
	return hsID, err
}

// GetUserNameAndHSByID returns (username, headscale_user_id) for a
// given user id. Used by PostAdminDeleteUser, which needs the name
// for the audit log and the hs id to call headscale's delete. The
// username-first order matches the call site. Returns
// ErrUserNotFound if no row.
func GetUserNameAndHSByID(d *sql.DB, id int64) (string, sql.NullInt64, error) {
	var name string
	var hsID sql.NullInt64
	err := d.QueryRow(qSelectUserByID, id).Scan(&name, &hsID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.NullInt64{}, ErrUserNotFound
	}
	return name, hsID, err
}

// GetUserHSByID returns (headscale_user_id, username) for a given
// user id. Used by GetMyDevices and PostMyPreauth, both of which
// need the hs id first (it's the "if we don't have this, fail 400"
// gate) and the username second (for the audit log). Order matches
// the SQL and the call sites.
func GetUserHSByID(d *sql.DB, id int64) (sql.NullInt64, string, error) {
	var hsID sql.NullInt64
	var username string
	err := d.QueryRow(qSelectUserHSByID, id).Scan(&hsID, &username)
	return hsID, username, err
}

// GetAllPortalUsers returns every portal user, ordered by id. Used
// by GetAdminUsers to render the /admin/users page. The User struct's
// PasswordHash is left empty (the SELECT doesn't ask for it, and we
// never want to leak hashes to the template).
func GetAllPortalUsers(d *sql.DB) ([]User, error) {
	rows, err := d.Query(qSelectAllPortalUsers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var adminI int
		var hsID sql.NullInt64
		var createdI int64
		var theme sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &adminI, &hsID, &createdI, &theme); err != nil {
			return nil, err
		}
		u.IsAdmin = adminI == 1
		u.HeadscaleUserID = hsID.Int64
		u.CreatedAt = time.Unix(createdI, 0)
		if theme.Valid {
			u.Theme = theme.String
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetPortalUsernames returns every portal username in id order.
// Used by GenerateACL to build the tagOwners section of the headscale
// policy (every portal user gets to own their tag:private).
func GetPortalUsernames(d *sql.DB) ([]string, error) {
	rows, err := d.Query(qSelectPortalUsernames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetOtherHSUserIDs returns the headscale_user_id values of every
// portal user EXCEPT excludeID, skipping NULLs and empty strings.
// Used by backfillNodeOwnership to build a "is this node already
// claimed by someone else" lookup. Returns an empty slice if no
// other users have a hs id (the common case on a fresh install).
func GetOtherHSUserIDs(d *sql.DB, excludeID int64) ([]string, error) {
	rows, err := d.Query(qSelectOtherHSUserIDs, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InsertPortalUser creates one row in portal_users. Returns the new
// row's id. Used by PostAdminUser after a successful headscale user
// create. The (is_admin bool → int) and (hsID int64 → 0 is fine) are
// the two bits of conversion the SQL doesn't have to know about.
func InsertPortalUser(d *sql.DB, username, passwordHash string, isAdmin bool, hsID int64) (int64, error) {
	adminI := 0
	if isAdmin {
		adminI = 1
	}
	res, err := d.Exec(qInsertPortalUser, username, passwordHash, adminI, hsID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePasswordHash sets a new password_hash for a user. Used by
// PostMyAccount (self-service change) and PostAdminUserResetPassword
// (admin-triggered reset). Returns the number of rows affected so
// callers can detect "user vanished between auth and update" — most
// callers ignore it, but it's free to expose.
func UpdatePasswordHash(d *sql.DB, id int64, passwordHash string) (int64, error) {
	res, err := d.Exec(qUpdatePasswordHash, passwordHash, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeletePortalUserByID removes the row for `id`. The PostAdminDeleteUser
// handler is responsible for cleaning up dependent tables
// (preauth_keys, audit_log, personal_api_tokens) — this helper only
// touches portal_users because the dependent cleanup order is
// handler-policy, not a pure DB concern.
func DeletePortalUserByID(d *sql.DB, id int64) (int64, error) {
	res, err := d.Exec(qDeletePortalUserByID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
