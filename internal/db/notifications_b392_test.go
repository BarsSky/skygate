// B392 (2026-10-16) — the /my/notifications page must page and count in SQL.
//
// The pre-B392 behaviour this pins down: the handler fetched a capped slice into
// memory and sliced it, and the db helpers silently rewrote any limit > 100 to 50
// (the page asked for 200 and got 50). Consequences measured on the code:
//   - "N total" could never exceed 50, so the counter lied about the real history;
//   - every page past the second rendered the "you have no notifications" empty
//     state while the rows were still in the table.
//
// The query uses optional conditions ($2 = 0 → all rows, $3 = ” → any severity)
// rather than one query per combination, so the page, the total and the
// "has next" decision can never disagree. That form is exercised on SQLite here
// and, when SKYGATE_TEST_PG_DSN is set, on PostgreSQL too — the placeholder shape
// ($N repeated at most twice, highest N == len(args)) is the one internal/db/placeholders.go
// documents as valid on both drivers.
package db

import (
	"database/sql"
	"fmt"
	"testing"
)

// newNotificationsTestDB builds a migrated SQLite database with one user and a
// controllable number of notifications.
func newNotificationsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:notifications_b392?mode=memory&cache=shared")
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS notifications (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		type TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '',
		link TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		read_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create notifications: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM notifications`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return d
}

// seedNotifications inserts n rows for userID: every third one unread, severities
// cycling info/warn/danger, created_at increasing with the index so the newest is
// the highest id.
func seedNotifications(t *testing.T, d *sql.DB, userID int64, n int) {
	t.Helper()
	sevs := []string{"info", "warn", "danger"}
	for i := 0; i < n; i++ {
		// Every third row stays unread (read_at = 0).
		readAt := int64(0)
		if i%3 != 0 {
			readAt = int64(1000 + i)
		}
		if _, err := d.Exec(`INSERT INTO notifications (user_id, type, severity, title, body, link, created_at, read_at)
			VALUES (?, 'key.expiring', ?, ?, '', '', ?, ?)`,
			userID, sevs[i%3], fmt.Sprintf("n%d", i), int64(i+1), readAt); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
}

// TestNotificationsPageAndCount_B392 — a page returns exactly the requested
// window, and the total is the real row count.
func TestNotificationsPageAndCount_B392(t *testing.T) {
	d := newNotificationsTestDB(t)
	seedNotifications(t, d, 1, 120)

	total, err := CountNotificationsByUser(d, 1, NotificationFilterAll, "")
	if err != nil {
		t.Fatalf("Count all: %v", err)
	}
	if total != 120 {
		t.Errorf("total = %d, want 120 (pre-B392 this was capped at 50)", total)
	}

	// Page 3 of 25 → rows 51..75, newest first.
	page, err := ListNotificationsByUserPage(d, 1, NotificationFilterAll, "", 25, 50)
	if err != nil {
		t.Fatalf("List page 3: %v", err)
	}
	if len(page) != 25 {
		t.Fatalf("page 3 has %d rows, want 25 (page 3 used to render the empty state)", len(page))
	}
	if page[0].Title != "n69" { // 120 - 50 - 1 = 69
		t.Errorf("page 3 first row = %q, want n69 (newest first, offset honoured)", page[0].Title)
	}

	// The last page is partial and must not be reported as absent.
	last, err := ListNotificationsByUserPage(d, 1, NotificationFilterAll, "", 25, 100)
	if err != nil {
		t.Fatalf("List last page: %v", err)
	}
	if len(last) != 20 {
		t.Errorf("last page has %d rows, want 20", len(last))
	}

	// One past the end is empty, and the caller can tell that apart from a page
	// that exists (that is what the total is for).
	past, err := ListNotificationsByUserPage(d, 1, NotificationFilterAll, "", 25, 120)
	if err != nil {
		t.Fatalf("List past end: %v", err)
	}
	if len(past) != 0 {
		t.Errorf("page past the end has %d rows, want 0", len(past))
	}
}

// TestNotificationsUnreadFilter_B392 — the unread filter is applied in SQL, and
// its total is the unread total (not the size of a capped slice).
func TestNotificationsUnreadFilter_B392(t *testing.T) {
	d := newNotificationsTestDB(t)
	seedNotifications(t, d, 1, 60)

	unreadTotal, err := CountNotificationsByUser(d, 1, NotificationFilterUnread, "")
	if err != nil {
		t.Fatalf("Count unread: %v", err)
	}
	if unreadTotal != 20 { // every third row is unread
		t.Errorf("unread total = %d, want 20", unreadTotal)
	}
	allTotal, err := CountNotificationsByUser(d, 1, NotificationFilterAll, "")
	if err != nil {
		t.Fatalf("Count all: %v", err)
	}
	if allTotal != 60 {
		t.Errorf("all total = %d, want 60", allTotal)
	}
	page, err := ListNotificationsByUserPage(d, 1, NotificationFilterUnread, "", 10, 0)
	if err != nil {
		t.Fatalf("List unread: %v", err)
	}
	for _, n := range page {
		// ReadAt is zero exactly when read_at = 0 in the row (unread).
		if !n.ReadAt.IsZero() {
			t.Errorf("unread page returned a read row: id=%d read_at=%v", n.ID, n.ReadAt)
		}
	}
	if len(page) != 10 {
		t.Errorf("unread page has %d rows, want 10", len(page))
	}
}

// TestNotificationsSeverityFilter_B392 — the severity filter is a SQL condition,
// so the total matches the rows the page can actually show. This is the part that
// would be wrong if the filter were applied in Go over one fetched page: the
// counter would say "3 of 120" while the next page could contain more warns.
func TestNotificationsSeverityFilter_B392(t *testing.T) {
	d := newNotificationsTestDB(t)
	seedNotifications(t, d, 1, 90) // 30 of each severity, newest first

	for _, sev := range []string{"info", "warn", "danger"} {
		total, err := CountNotificationsByUser(d, 1, NotificationFilterAll, sev)
		if err != nil {
			t.Fatalf("Count %s: %v", sev, err)
		}
		if total != 30 {
			t.Errorf("severity %s total = %d, want 30", sev, total)
		}
		// Walk every page of the filtered view and check the rows really match.
		seen := 0
		for offset := 0; offset < total; offset += 25 {
			page, err := ListNotificationsByUserPage(d, 1, NotificationFilterAll, sev, 25, offset)
			if err != nil {
				t.Fatalf("List %s offset %d: %v", sev, offset, err)
			}
			for _, n := range page {
				if n.Severity != sev {
					t.Errorf("severity filter %s returned a %s row", sev, n.Severity)
				}
				seen++
			}
		}
		if seen != total {
			t.Errorf("severity %s: walked %d rows, total says %d", sev, seen, total)
		}
	}

	// An unknown severity must behave as "any" (SeverityFor normalises it), and
	// the db layer must treat '' as "no condition" — never as "match nothing".
	anyTotal, err := CountNotificationsByUser(d, 1, NotificationFilterAll, "")
	if err != nil {
		t.Fatalf("Count any: %v", err)
	}
	if anyTotal != 90 {
		t.Errorf("empty severity total = %d, want 90 (must mean 'any')", anyTotal)
	}
}

// TestNotificationLimitIsNotSilentlyReduced_B392 — the helpers used to rewrite any
// limit above 100 to 50, so a caller could not tell a clamped read from a complete
// one. The ceiling is still bounded, but it is explicit and above what the bell and
// the page ask for.
func TestNotificationLimitIsNotSilentlyReduced_B392(t *testing.T) {
	d := newNotificationsTestDB(t)
	seedNotifications(t, d, 1, 120)

	got, err := ListNotificationsByUser(d, 1, 120)
	if err != nil {
		t.Fatalf("List 120: %v", err)
	}
	if len(got) != 120 {
		t.Errorf("asked for 120 rows, got %d (the old clamp returned 50)", len(got))
	}
	if clampNotificationLimit(0) != defaultNotificationLimit {
		t.Errorf("limit 0 = %d, want the documented default %d", clampNotificationLimit(0), defaultNotificationLimit)
	}
	if clampNotificationLimit(10000) != maxNotificationLimit {
		t.Errorf("limit 10000 = %d, want the documented ceiling %d", clampNotificationLimit(10000), maxNotificationLimit)
	}
	if maxNotificationLimit <= 120 {
		t.Errorf("the ceiling (%d) must be above what the UI asks for (120)", maxNotificationLimit)
	}
}
