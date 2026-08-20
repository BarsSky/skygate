// 2026-08-20 (B157, v1.5.0) — in-web notification inbox helpers.
//
// The notifications table (V059PG) is the user-facing
// mirror of the B156 keynotify scheduler: every
// expiring-key event the scheduler finds ALSO writes
// a row here (in addition to the Telegram send), so
// the user sees a per-page notification even if they
// don't have Telegram bound, or if the Telegram send
// was skipped for some reason.
//
// This file is a thin façade over the typed wrappers
// in internal/db/notifications.go. The re-export via
// a type alias (type Notification = db.Notification)
// keeps the caller API clean — they can use the
// notifications package without pulling in internal/db.

package notifications

import (
	"database/sql"

	"skygate/internal/db"
)

// Severity is the urgency level of a notification.
// Mirrors the B153/B155 badge class names so the
// bell's list view can reuse the same CSS colours.
const (
	SeverityInfo   = "info"   // neutral; default
	SeverityWarn   = "warn"   // yellow
	SeverityDanger = "danger" // red
)

// Type is the kind of notification. The string is
// open-ended (no PG enum) so future B-checks can
// add new kinds without a schema migration.
const (
	TypeKeyExpiring = "key.expiring"
	// Future: TypeCertRenewal = "cert.renewal" (B148 follow-up)
	// Future: TypeBackupFailed = "backup.failed" (B142 follow-up)
)

// Notification is the typed view of one row in
// the notifications table. Re-exported from the
// db package via a type alias so callers don't
// have to import internal/db. The IsRead method
// is on db.Notification itself.
type Notification = db.Notification

// InsertNotification writes a new notifications row.
// Returns the new id (mostly for tests; the bell
// doesn't need it).
//
// 2026-08-20 (B157): the typed wrapper for the
// qInsertNotification SQL constant. Created by
// the B156 keynotify scheduler when it finds an
// expiring key, AND by the future B157 admin
// "send broadcast" handler (a future operator
// tool to push a "v1.5.0 is live" notification to
// every user).
func InsertNotification(d *sql.DB, n db.Notification) (int64, error) {
	return db.InsertNotification(d, n)
}

// ListByUser returns every notification for userID,
// newest first. The bell uses this for the
// dropdown. limit caps the result; 0 = no cap.
func ListByUser(d *sql.DB, userID int64, limit int) ([]db.Notification, error) {
	return db.ListNotificationsByUser(d, userID, limit)
}

// ListUnreadByUser returns just the unread
// notifications for the bell's dropdown.
func ListUnreadByUser(d *sql.DB, userID int64, limit int) ([]db.Notification, error) {
	return db.ListUnreadNotificationsByUser(d, userID, limit)
}

// CountUnread returns the number of unread
// notifications for userID. The bell's badge
// shows this number; the layout's hot path
// (every page render) calls this so the count
// is always fresh.
func CountUnread(d *sql.DB, userID int64) (int, error) {
	return db.CountUnreadNotifications(d, userID)
}

// MarkRead sets read_at = now for the notification
// with the given id, scoped to userID (the user
// can only mark their OWN notifications as read).
func MarkRead(d *sql.DB, id, userID int64) (int64, error) {
	return db.MarkNotificationRead(d, id, userID)
}

// MarkAllRead sets read_at = now for every unread
// notification belonging to userID.
func MarkAllRead(d *sql.DB, userID int64) (int64, error) {
	return db.MarkAllNotificationsRead(d, userID)
}

// DeleteForUser removes every notifications row
// for the given user. The FK ON DELETE CASCADE
// handles this automatically; this helper is
// exposed for symmetry with the other portal_users
// cascade helpers.
func DeleteForUser(d *sql.DB, userID int64) (int64, error) {
	return db.DeleteNotificationsForUser(d, userID)
}
