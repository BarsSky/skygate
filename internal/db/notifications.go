// 2026-08-20 (B157, v1.5.0) — typed wrappers over the
// q* SQL constants for the notifications table
// (V059PG). Other packages (notifications,
// keynotify, feature/my) call these wrappers
// instead of touching the SQL directly — keeps
// the column list in one place and the q*
// constants private to this file.
//
// Pattern mirrors the other db/<table>.go files
// (preauth_keys.go, personal_api_tokens.go, etc):
// the file holds the typed view + the wrappers,
// the q* SQL strings live in queries.go.
//
// Note: the function names here are UNEXPORTED
// (lowercase first letter) because they wrap
// unexported q* constants. The public façade is
// in the notifications package (which calls these
// via internal-package access — wait, that's a
// problem; the notifications package is in a
// different package, so it can't see these).
//
// Resolution: the public wrappers live in the
// notifications package. Each wrapper here is a
// simple db-package function. The notifications
// package re-exports the typed view (Notification
// type) + the helpers. This is a 2-file split
// to keep the SQL private to the db package while
// letting other packages use the typed API.

package db

import (
	"database/sql"
	"time"
)

// dbInsertNotification writes a new notifications row.
// Package-private; called by InsertNotification in
// the notifications package.
func dbInsertNotification(d *sql.DB, n Notification) (int64, error) {
	var id int64
	createdAt := n.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	err := d.QueryRow(qInsertNotification,
		n.UserID, n.Type, n.Severity, n.Title, n.Body, n.Link, createdAt.Unix(),
	).Scan(&id)
	return id, err
}

// dbListNotificationsByUser returns every
// notification for userID, newest first.
func dbListNotificationsByUser(d *sql.DB, userID int64, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := d.Query(qListNotificationsByUser, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var createdSec, readSec int64
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Severity, &n.Title, &n.Body, &n.Link, &createdSec, &readSec); err != nil {
			return nil, err
		}
		if createdSec > 0 {
			n.CreatedAt = time.Unix(createdSec, 0)
		}
		if readSec > 0 {
			n.ReadAt = time.Unix(readSec, 0)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// dbListUnreadNotificationsByUser returns just the
// unread notifications for the bell's dropdown.
func dbListUnreadNotificationsByUser(d *sql.DB, userID int64, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := d.Query(qListUnreadNotificationsByUser, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var createdSec, readSec int64
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Severity, &n.Title, &n.Body, &n.Link, &createdSec, &readSec); err != nil {
			return nil, err
		}
		if createdSec > 0 {
			n.CreatedAt = time.Unix(createdSec, 0)
		}
		if readSec > 0 {
			n.ReadAt = time.Unix(readSec, 0)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// dbCountUnreadNotifications returns the number of
// unread notifications for userID.
func dbCountUnreadNotifications(d *sql.DB, userID int64) (int, error) {
	var n int
	err := d.QueryRow(qCountUnreadNotifications, userID).Scan(&n)
	return n, err
}

// dbMarkNotificationRead sets read_at = now for the
// notification with the given id, scoped to userID.
func dbMarkNotificationRead(d *sql.DB, id, userID int64) (int64, error) {
	res, err := d.Exec(qMarkNotificationRead, time.Now().Unix(), id, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// dbMarkAllNotificationsRead sets read_at = now for
// every unread notification belonging to userID.
func dbMarkAllNotificationsRead(d *sql.DB, userID int64) (int64, error) {
	res, err := d.Exec(qMarkAllNotificationsRead, time.Now().Unix(), userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// dbDeleteNotificationsForUser removes every
// notifications row for the given user.
func dbDeleteNotificationsForUser(d *sql.DB, userID int64) (int64, error) {
	res, err := d.Exec(qDeleteNotificationsForUser, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- EXPORTED wrappers for cross-package use ---
//
// The notifications package re-exports the type + calls
// these wrappers. Pattern: the q* SQL constants stay
// private to this file; the exported wrappers (with
// the same names + uppercase first letter) are the
// public API. Mirrors InsertPreauthKey /
// ListPreauthKeysByUser in preauth_keys.go.

// InsertNotification writes a new notifications row.
// Returns the new id (mostly for tests; the bell
// doesn't need it).
func InsertNotification(d *sql.DB, n Notification) (int64, error) {
	return dbInsertNotification(d, n)
}

// ListNotificationsByUser returns every
// notification for userID, newest first.
func ListNotificationsByUser(d *sql.DB, userID int64, limit int) ([]Notification, error) {
	return dbListNotificationsByUser(d, userID, limit)
}

// ListUnreadNotificationsByUser returns just the
// unread notifications for the bell's dropdown.
func ListUnreadNotificationsByUser(d *sql.DB, userID int64, limit int) ([]Notification, error) {
	return dbListUnreadNotificationsByUser(d, userID, limit)
}

// CountUnreadNotifications returns the number of
// unread notifications for userID.
func CountUnreadNotifications(d *sql.DB, userID int64) (int, error) {
	return dbCountUnreadNotifications(d, userID)
}

// MarkNotificationRead sets read_at = now for the
// notification with the given id, scoped to userID.
func MarkNotificationRead(d *sql.DB, id, userID int64) (int64, error) {
	return dbMarkNotificationRead(d, id, userID)
}

// MarkAllNotificationsRead sets read_at = now for
// every unread notification belonging to userID.
func MarkAllNotificationsRead(d *sql.DB, userID int64) (int64, error) {
	return dbMarkAllNotificationsRead(d, userID)
}

// DeleteNotificationsForUser removes every
// notifications row for the given user.
func DeleteNotificationsForUser(d *sql.DB, userID int64) (int64, error) {
	return dbDeleteNotificationsForUser(d, userID)
}

// Notification is the typed view of one row in
// the notifications table. Used by the bell's
// dropdown list + the unread-count badge.
//
// EXPORTED so the notifications package can use
// the type in its public API (the wrappers there
// build + return []Notification).
type Notification struct {
	ID        int64
	UserID    int64
	Type      string
	Severity  string
	Title     string
	Body      string
	Link      string
	CreatedAt time.Time
	ReadAt    time.Time
}

// IsRead returns true if the notification has been
// marked as read by the user.
func (n Notification) IsRead() bool {
	return !n.ReadAt.IsZero()
}
