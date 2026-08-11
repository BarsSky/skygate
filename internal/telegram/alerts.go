package telegram

// alerts.go — SendAlert extension to Notifier + /ack bookkeeping.
//
// Phase 3 (/exit_nodes, /quota, /ack) needs alerts to be addressable
// so the admin can dismiss them from a phone. To make /ack work
// every operational trigger now goes through SendAlert instead of
// SendTelegram: SendAlert inserts a row into telegram_alerts, takes
// the rowid, and prefixes the outgoing message with [#<id>] so the
// admin sees the id they can reference.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"

	"skygate/internal/db"
)

// alertsKeep is the cap on telegram_alerts rows. We prune older rows
// on every insert; exact bound is not important — once an alert is
// acked (or stale) it has no value, and http.StatusInternalServerError rows covers weeks of
// activity under the current trigger rate.
const alertsKeep = http.StatusInternalServerError

// SendAlert posts text as an alert (i.e. as a numbered row in
// telegram_alerts) and returns the id that /ack can reference.
//
// Returns 0 when the notifier is not configured to send (admin
// hasn't saved a token + chat_id yet) — in that case we don't
// write to the table either, because an alert nobody can see
// shouldn't pollute the ack list.
//
// 2026-07-13: switched from Configured() (token-only) to
// LoadTelegramSendTarget (token + chat_id) — SendAlert posts
// to a specific chat, so it needs the chat_id. The
// "Configured-or-send" check would silently drop alerts.
func (n *RealNotifier) SendAlert(text string) int64 {
	if n == nil {
		return 0
	}
	_, _, ok, err := db.LoadTelegramSendTarget(n.db)
	if err != nil || !ok {
		return 0
	}
	id, err := insertAlert(n.db, text)
	if err != nil {
		log.Printf("telegram: alert insert failed: %v", err)
		// fall through and send the un-prefixed message so the
		// operator still gets the signal (just without an id).
	} else {
		text = fmt.Sprintf("[#%d] %s", id, text)
	}
	n.SendTelegram(text)
	// Fire-and-forget prune. Failure here is harmless; the next
	// SendAlert will try again.
	go pruneAlerts(n.db, alertsKeep)
	return id
}

// SendAlert on NoopNotifier always returns 0; the row is never
// written because there is no configured bot to see it.
func (NoopNotifier) SendAlert(string) int64 { return 0 }

// insertAlert writes a new row to telegram_alerts. v0.32.27:
// uses RETURNING id (works for both SQLite 3.35+ and PG).
// v0.33.1.12: switched from hardcoded "$1" to
// db.PlaceholdersList(1) so SQLite mode (which the dev
// Makefile defaults to) doesn't crash with "near '$1':
// syntax error".
func insertAlert(d *sql.DB, body string) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO telegram_alerts(body) VALUES (`+db.PlaceholdersList(1)+`) RETURNING id`, body).Scan(&id)
	return id, err
}

// pruneAlerts keeps at most maxRows in telegram_alerts. We delete
// any rows older than the Nth-from-the-top (so the most recent
// maxRows survive). Cheaper than a full COUNT on every send.
// v0.33.1.12: switched from hardcoded "?" to
// db.PlaceholdersList(1) for PG compat.
func pruneAlerts(d *sql.DB, maxRows int) {
	if maxRows <= 0 {
		return
	}
	_, _ = d.Exec(`
		DELETE FROM telegram_alerts
		 WHERE id NOT IN (
			SELECT id FROM telegram_alerts
			 ORDER BY id DESC
			 LIMIT `+db.PlaceholdersList(1)+`
		 )`, maxRows)
}

// formatAlertRow is a one-line summary for /ack reply. We trim the
// body so the ack confirmation fits in one Telegram line.
func formatAlertRow(id int64, body string) string {
	body = strings.TrimSpace(body)
	body = strings.ReplaceAll(body, "\n", " ")
	if len(body) > 120 {
		body = body[:117] + "..."
	}
	return fmt.Sprintf("[#%d] %s", id, body)
}
