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
	"strings"
)

// alertsKeep is the cap on telegram_alerts rows. We prune older rows
// on every insert; exact bound is not important — once an alert is
// acked (or stale) it has no value, and 500 rows covers weeks of
// activity under the current trigger rate.
const alertsKeep = 500

// SendAlert posts text as an alert (i.e. as a numbered row in
// telegram_alerts) and returns the id that /ack can reference.
//
// Returns 0 when the notifier is not configured (admin hasn't saved
// a token yet) — in that case we don't write to the table either,
// because an alert nobody can see shouldn't pollute the ack list.
func (n *RealNotifier) SendAlert(text string) int64 {
	if n == nil {
		return 0
	}
	if !n.Configured() {
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

func insertAlert(d *sql.DB, body string) (int64, error) {
	res, err := d.Exec(`INSERT INTO telegram_alerts(body) VALUES (?)`, body)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// pruneAlerts keeps at most maxRows in telegram_alerts. We delete
// any rows older than the Nth-from-the-top (so the most recent
// maxRows survive). Cheaper than a full COUNT on every send.
func pruneAlerts(d *sql.DB, maxRows int) {
	if maxRows <= 0 {
		return
	}
	_, _ = d.Exec(`
		DELETE FROM telegram_alerts
		 WHERE id NOT IN (
			SELECT id FROM telegram_alerts
			 ORDER BY id DESC
			 LIMIT ?
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
