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
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/localtime"
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

// ListPageByFilter returns one page of notifications for the /my/notifications
// page, newest first, paginated IN SQL (B392). filter is "all" or "unread",
// severity is one of the severity constants or "" for any.
func ListPageByFilter(d *sql.DB, userID int64, filter, severity string, limit, offset int) ([]db.Notification, error) {
	return db.ListNotificationsByUserPage(d, userID, NotificationFilterFor(filter), SeverityFor(severity), limit, offset)
}

// CountByFilter returns how many rows the filter covers — the real total, not
// the size of a capped slice (B392).
func CountByFilter(d *sql.DB, userID int64, filter, severity string) (int, error) {
	return db.CountNotificationsByUser(d, userID, NotificationFilterFor(filter), SeverityFor(severity))
}

// SeverityFor normalises a user-supplied severity value. Anything unknown means
// "any severity", so a bad query string can never hide rows.
func SeverityFor(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case SeverityInfo:
		return SeverityInfo
	case SeverityWarn:
		return SeverityWarn
	case SeverityDanger:
		return SeverityDanger
	}
	return ""
}

// NotificationFilterFor normalises a user-supplied filter value. Anything that
// is not "unread" means "all", so an unknown value can never hide rows.
func NotificationFilterFor(filter string) db.NotificationFilter {
	if filter == string(db.NotificationFilterUnread) {
		return db.NotificationFilterUnread
	}
	return db.NotificationFilterAll
}

// View is one bell-dropdown row: the stored notification plus the two
// presentation fields the template needs. B392 added it because the dropdown
// used to render the raw row, which meant the item had no timestamp at all
// (only the full page had one) and the icons were computed in the page handler.
type View struct {
	db.Notification
	// TimeAgo is the localised "5 min ago" line.
	TimeAgo string
	// Icon is the FontAwesome class for the row's type.
	Icon string
}

// NewView builds the presentation view for one notification.
func NewView(lang string, n db.Notification, ref time.Time) View {
	return View{
		Notification: n,
		TimeAgo:      TimeAgo(lang, n.CreatedAt, ref),
		Icon:         TypeIcon(n.Type),
	}
}

// NewViews builds the presentation views for a list, preserving the order.
func NewViews(lang string, list []db.Notification, ref time.Time) []View {
	out := make([]View, 0, len(list))
	for _, n := range list {
		out = append(out, NewView(lang, n, ref))
	}
	return out
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

// --- B157.1 pure-function helpers ---

// TimeAgo returns a human-friendly "ago" string for a notification's CreatedAt
// timestamp, in the caller's language.
//
// 2026-10-16 (B392): this was English-only ("2 h ago" inside a Russian panel)
// even though the notif.time_* keys existed in both catalogues and were simply
// never used. The language is now a parameter, like trimForTelegram's.
//
// Examples (ref=now, lang=en):
//
//	30 seconds ago → "just now"
//	5 minutes ago  → "5 min ago"
//	2 hours ago    → "2 h ago"
//	1 day ago      → "1 d ago"
//	3 days ago     → "3 d ago"
//	2 months ago   → "2 mo ago"
//	older          → "YYYY-MM-DD" (raw date)
//
// The thresholds are tuned for the B156 keynotify scheduler's daily 9 AM run:
// most notifications are "today" / "yesterday" / "this week", so the verbose raw
// date only kicks in for old items (the user scrolled deep into the list).
func TimeAgo(lang string, t time.Time, ref time.Time) string {
	if t.IsZero() {
		return ""
	}
	delta := ref.Sub(t)
	if delta < 0 {
		// Future timestamp (clock skew). Show
		// as "just now" rather than negative
		// time.
		delta = 0
	}
	secs := int(delta.Seconds())
	switch {
	case secs < 45:
		return i18n.T(lang, "notif.time_just_now")
	case secs < 90:
		return i18n.Tf(lang, "notif.time_min_ago", 1)
	case secs < 60*60:
		return i18n.Tf(lang, "notif.time_min_ago", secs/60)
	case secs < 2*60*60:
		return i18n.Tf(lang, "notif.time_h_ago", 1)
	case secs < 24*60*60:
		return i18n.Tf(lang, "notif.time_h_ago", secs/(60*60))
	case secs < 2*24*60*60:
		return i18n.Tf(lang, "notif.time_d_ago", 1)
	case secs < 30*24*60*60:
		return i18n.Tf(lang, "notif.time_d_ago", secs/(24*60*60))
	case secs < 60*24*60*60:
		// 1-2 months — show as weeks.
		return i18n.Tf(lang, "notif.time_wk_ago", secs/(7*24*60*60))
	case secs < 365*24*60*60:
		return i18n.Tf(lang, "notif.time_mo_ago", secs/(30*24*60*60))
	}
	// Older than 1 year — show raw date so
	// the user has a precise reference.
	return localtime.FormatDate(t)
}

// TypeIcon returns the FontAwesome 5/6 icon class
// for a notification Type. Used by the bell
// dropdown + the full /my/notifications page so
// each notification kind has a distinct visual
// signature (key icon for key.expiring, etc).
//
// Returns "fa-bell" as the fallback for unknown
// types so a future B-check that adds a new type
// without updating this switch still renders
// something reasonable (just a generic bell).
func TypeIcon(t string) string {
	switch t {
	case TypeKeyExpiring:
		return "fa-key"
	// Future: case TypeCertRenewal: return "fa-certificate"
	// Future: case TypeBackupFailed:  return "fa-triangle-exclamation"
	default:
		return "fa-bell"
	}
}

// TypeSeverityColor returns the B153/B155
// badge-* class suffix for a notification's
// Severity. The bell's per-item icon background
// uses the same colour family so the user
// learns the visual mapping between severity
// and colour (info=blue, warn=yellow,
// danger=red). Returns the empty string for
// unknown severities so the template's class
// concatenation doesn't produce a broken CSS
// class.
func TypeSeverityColor(severity string) string {
	switch severity {
	case SeverityInfo:
		return "info"
	case SeverityWarn:
		return "warn"
	case SeverityDanger:
		return "danger"
	default:
		return ""
	}
}
