// 2026-08-20 (B157, v1.5.0) — in-web notification inbox
// handlers.
//
// Three endpoints:
//   - GET  /my/notifications           (future: dedicated inbox page; for B157
//                                       the bell is rendered in the layout
//                                       sidebar via UnreadCount, so this route
//                                       is reserved for a future full-page view)
//   - POST /my/notifications/{id}/read (mark single as read)
//   - POST /my/notifications/read-all (mark every unread as read)
//
// All three redirect back to the page that issued
// the request (via the Referer header, falling back
// to /dashboard) so the user's place in the UI is
// preserved. The bell re-renders with the new
// unread count on the next page load.

package my

import (
	"net/http"
	"strconv"
	"time"

	"skygate/internal/notifications"
)

// GetMyNotifications renders the full /my/notifications
// page (B157.1). B157's bell dropdown is the
// always-visible "what's new" view; this page is
// the "show me everything, including what I've
// already dismissed" view.
//
// Filter:
//   - ?filter=all     (default) — every notification,
//     newest first, paginated.
//   - ?filter=unread  — only unread (the bell's
//     equivalent).
//
// Pagination: ?page=N (1-indexed, default 1). Page
// size is hardcoded to 25 — the page is a
// long-scrolling list, no need for a complex
// pagination UI. The "next page" link is shown
// when the result count equals the page size
// (a small over-fetch is acceptable; the user
// just sees one extra empty row).
//
// The page auto-includes UnreadCount + the
// "Unread (N)" filter pill so the user can
// jump back to the unread-only view in one
// click.
func (s *Service) GetMyNotifications(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	lang := s.I18n.LangFromRequest(r)

	// Filter + page.
	filter := r.URL.Query().Get("filter")
	if filter != "unread" {
		filter = "all"
	}
	page := 1
	if pStr := r.URL.Query().Get("page"); pStr != "" {
		if p, perr := strconv.Atoi(pStr); perr == nil && p > 0 && p < 1000 {
			page = p
		}
	}
	const pageSize = 25

	// Load the list. The list view always uses
	// ListByUser (read + unread) so the user can
	// scroll back through history. The "unread"
	// filter is applied in Go (just an `if` in
	// the render loop) so we don't need a
	// second SQL helper — a single SELECT +
	// in-memory filter is cheaper than a second
	// round-trip for the 50-row cap.
	all, err := notifications.ListByUser(s.DB, c.UserID, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Pre-compute the "TimeAgo" for each row +
	// the type icon. Done in Go (not in the
	// template) so the helper stays testable +
	// the template stays presentation-only.
	type Row struct {
		notifications.Notification
		TimeAgo string
		Icon    string
	}
	rows := make([]Row, 0, len(all))
	now := time.Now()
	for _, n := range all {
		rows = append(rows, Row{
			Notification: n,
			TimeAgo:      notifications.TimeAgo(n.CreatedAt, now),
			Icon:         notifications.TypeIcon(n.Type),
		})
	}
	// Apply the unread filter (in-memory).
	filtered := rows
	if filter == "unread" {
		filtered = make([]Row, 0, len(rows))
		for _, r := range rows {
			if !r.IsRead() {
				filtered = append(filtered, r)
			}
		}
	}
	// Pagination slice. pageSize elements from
	// the start of `filtered`.
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	pageRows := filtered[start:end]
	hasNext := end < len(filtered)
	hasPrev := page > 1

	// Unread count for the filter pill.
	unreadCount, _ := notifications.CountUnread(s.DB, c.UserID)

	data := map[string]any{
		"Page":        "notifications",
		"Title":       "Notifications",
		"Filter":      filter,
		"Rows":        pageRows,
		"HasNext":     hasNext,
		"HasPrev":     hasPrev,
		"NextPage":    page + 1,
		"PrevPage":    page - 1,
		"TotalCount":  len(filtered),
		"UnreadCount": unreadCount,
		"Lang":        lang,
	}
	s.Backend.RenderWithLayout(w, r, "user/notifications.html", c, data)
}

// PostMyNotificationRead marks a single notification
// as read for the current user. The id is the
// notification row's id (parsed from the URL path).
// Returns 404 if the id doesn't belong to the user
// (a guard against id-probing). On success, redirects
// back to the Referer (or /dashboard) so the user
// stays on the page they came from.
func (s *Service) PostMyNotificationRead(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	rows, err := notifications.MarkRead(s.DB, id, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == 0 {
		// Either the row doesn't exist or it
		// doesn't belong to the current user.
		// Both are 404 from the bell's
		// perspective.
		http.Error(w, "notification not found", http.StatusNotFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "notif_read", strconv.FormatInt(id, 10))
	http.Redirect(w, r, refererOrDashboard(r), http.StatusFound)
}

// PostMyNotificationsReadAll marks every unread
// notification for the current user as read.
// On success, redirects back to the Referer
// (or /dashboard). The audit log gets a single
// row with the row-count so the operator can see
// "user X marked N notifications as read".
func (s *Service) PostMyNotificationsReadAll(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	rows, err := notifications.MarkAllRead(s.DB, c.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "notif_read_all", strconv.FormatInt(rows, 10))
	http.Redirect(w, r, refererOrDashboard(r), http.StatusFound)
}

// refererOrDashboard returns the Referer header
// value if it's a same-origin path (starts with /
// and doesn't contain //), or /dashboard as a
// safe fallback. Used by the mark-read handlers
// so the user stays on the page that issued the
// request.
func refererOrDashboard(r *http.Request) string {
	ref := r.Referer()
	if ref == "" {
		return "/dashboard"
	}
	// Defensive: only follow same-origin
	// relative paths. Without this, a Referer
	// like "https://evil.com" would redirect
	// the user to a malicious page after a
	// mark-read click. (Browsers strip the
	// path on cross-origin Referer, but a
	// same-host attacker could still craft
	// a /something URL that the operator
	// follows. Limiting to /-prefixed paths
	// is the standard CSRF/redirect defense.)
	if len(ref) < 1 || ref[0] != '/' {
		return "/dashboard"
	}
	// Reject "//" (protocol-relative URL) and
	// "/\..." (some browsers normalise these
	// to the host).
	if len(ref) >= 2 && (ref[1] == '/' || ref[1] == '\\') {
		return "/dashboard"
	}
	return ref
}
