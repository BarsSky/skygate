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
	"sync"
	"time"

	"skygate/internal/db"
)

// alertDedup is the process-local dedup table for composed alerts (B391). Keyed by
// Alert.DedupKey; see shouldSuppressAlert.
var alertDedup sync.Map

// ResetAlertDedupForTest empties the dedup table. Tests only — the table is process-local
// on purpose (one process per install), but that also means a test which uses a FIXED
// DedupKey is correct only on the first run: `go test ./internal/telegram -count=2` failed
// deterministically on TestB391_DedupSuppressesRepeatsInsideTheWindow with "the first alert
// of a key must be sent", because the entry from the previous run suppressed it. A test
// whose verdict depends on how many times the binary ran is not evidence, so the state it
// depends on is now resettable and reset (B426.1).
func ResetAlertDedupForTest() {
	alertDedup.Range(func(k, _ any) bool {
		alertDedup.Delete(k)
		return true
	})
}

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
//
// B391 (2026-10-25, TD-34): the legacy text path. The 60-odd call sites still pass a
// plain English sentence built with fmt.Sprintf, and they now get the two things they
// never had: the body is escaped and sent with parse_mode=HTML (so a `<hostname>` in a
// message cannot be rejected by the API, and no ``` fence mangles it), and it is
// bounded by the composer's length policy (a long alert used to be refused outright and
// LOST). New or changed alerts should build a ComposedAlert and call SendAlertHTML —
// the translation ratchet in scripts/check_b391_alert_composer.sh counts the rest.
func (n *RealNotifier) SendAlert(text string) int64 {
	if n == nil {
		return 0
	}
	// html.EscapeString is NOT applied here on purpose: the body is a plain-text
	// legacy message, and the send boundary (postMessageErr → sanitizeTelegramHTML)
	// escapes whatever is not markup we wrote. Doing it twice would show &amp;amp;.
	return n.sendAlert(SeverityInfo, text, "", "", 0)
}

// SendAlertHTML posts an ALREADY COMPOSED alert body (see ComposeAlert) as a numbered
// alert. The body is HTML the composer built, so it goes out with parse_mode=HTML and
// no fence; the send boundary still sanitises it (a call site may interpolate a
// hostname).
//
// `plain` is what /ack shows and what the log line carries — the same alert without
// markup, so the operator's ack list stays readable.
func (n *RealNotifier) SendAlertHTML(htmlBody, plain string) int64 {
	if n == nil {
		return 0
	}
	return n.sendAlert(SeverityInfo, htmlBody, plain, "", 0)
}

// SendAlertEvent is the fully-featured entry point: it composes the alert, honours its
// dedup key and sends it. Returns 0 when the bot is not configured OR the alert was
// suppressed as a repeat inside its dedup window (the caller can distinguish the two
// only by AlertDeliverySnapshot, which is what the disk monitor and the admin page do).
func (n *RealNotifier) SendAlertEvent(a Alert) int64 {
	if n == nil {
		return 0
	}
	if a.DedupKey != "" {
		if suppressed, count := shouldSuppressAlert(a.DedupKey, a.DedupTTL); suppressed {
			// B421: a suppression is a DECISION about an alert, so it belongs in
			// the ledger too — "the operator has already read this event" must
			// be distinguishable from "nothing was ever produced".
			recordAlertOutcome(time.Now(), AlertSuppressed,
				fmt.Sprintf("dedup key %q, %d repeat(s) in the window", a.DedupKey, count),
				0, "", "", "")
			return 0
		}
	}
	return n.sendAlert(a.Severity, ComposeAlert(a), ComposeAlertPlain(a), a.DedupKey, a.DedupTTL)
}

// sendAlert is the one implementation: resolve target → bookkeeping row → number →
// HTML send, with EVERY outcome recorded in the B421 ledger.
//
// B421 (2026-10-25): this function used to start with
//
//	if err != nil || !ok { return 0 }
//
// against db.LoadTelegramSendTarget — i.e. it required global_settings.telegram.chat_id
// and, when the operator had configured the bot by BINDING a chat instead (which is what
// /start + [Bind] does, and what “Send test” itself falls back to), it dropped every
// alert with no log line, no counter and no audit row. The bot answered commands, the
// test said “sent”, and notifications never arrived. resolveAlertTarget now applies ONE
// rule (global chat → ADMIN binding → nothing, named) and the ledger makes the result
// visible on /admin/telegram and in the journal.
//
// Returns the /ack alert id when the message was ACCEPTED by the Bot API, and 0 when it
// was not delivered for any reason — the ledger says which.
func (n *RealNotifier) sendAlert(sev Severity, htmlBody, plainBody, dedupKey string, _ time.Duration) int64 {
	if n == nil {
		return 0
	}
	tgt, outcome, reason := resolveAlertTarget(n.db)
	if outcome != AlertSent {
		recordAlertOutcome(time.Now(), outcome, reason, 0, "", tgt.source, maskChatID(tgt.chatID))
		return 0
	}
	masked := maskChatID(tgt.chatID)

	if plainBody == "" {
		plainBody = htmlBody
	}
	id, err := insertAlert(n.db, plainBody)
	if err != nil {
		// B426.1: name the CONSEQUENCE, not just the error. The message is still sent, so
		// this line is the only trace of why the caller got id=0 for a delivered alert —
		// and id=0 is what callers read as "not delivered" (the B391 disk monitor retries
		// on it). On SQLite the usual cause is a concurrent writer without a busy_timeout:
		// the send path inserts while `go pruneAlerts` deletes, measured at 237 of 400
		// concurrent writes lost before the timeout was added.
		log.Printf("telegram: alert insert failed: %v — the message is still sent, but it has no id, so the caller sees 0 (\"not delivered\")", err)
		// fall through and send the un-prefixed message so the
		// operator still gets the signal (just without an id).
	} else {
		htmlBody = fmt.Sprintf("[#%d] %s", id, htmlBody)
	}
	// The resolved TARGET is passed to the transport, which no longer re-reads
	// global_settings: re-reading here was the defect (the reply path had a
	// bound-chat fallback and this one did not, so the two disagreed exactly when a
	// notification mattered).
	if err := n.sendHTMLToChatTarget(tgt.token, tgt.chatID, htmlBody); err != nil {
		recordAlertOutcome(time.Now(), AlertSendFailed, RedactToken(err.Error(), tgt.token), 0, "", tgt.source, masked)
		log.Printf("telegram: alert send failed (severity=%s): %v", sev, err)
		return 0
	}
	recordAlertOutcome(time.Now(), AlertSent, "", id, alertTransportPath(n.EgressSnapshot()), tgt.source, masked)
	// Fire-and-forget prune. Failure here is harmless; the next
	// SendAlert will try again.
	go pruneAlerts(n.db, alertsKeep)
	return id
}

// SendAlert on NoopNotifier always returns 0; the row is never
// written because there is no configured bot to see it.
func (NoopNotifier) SendAlert(string) int64 { return 0 }

// SendAlertHTML on NoopNotifier returns 0: no bot, no row, no id.
func (NoopNotifier) SendAlertHTML(string, string) int64 { return 0 }

// SendAlertEvent on NoopNotifier returns 0.
func (NoopNotifier) SendAlertEvent(Alert) int64 { return 0 }

// shouldSuppressAlert answers "did we already send this within the window?" and counts
// the repeats. It is process-local by design: the point is to keep a flapping relay
// from flooding a chat, not to persist an incident history (the audit log and
// telegram_alerts do that).
func shouldSuppressAlert(key string, ttl time.Duration) (bool, int) {
	if ttl <= 0 {
		ttl = defaultAlertDedupTTL
	}
	now := time.Now()
	if v, ok := alertDedup.Load(key); ok {
		if e, ok := v.(alertDedupEntry); ok && now.Sub(e.at) < ttl {
			e.repeats++
			alertDedup.Store(key, e)
			return true, e.repeats
		}
	}
	alertDedup.Store(key, alertDedupEntry{at: now})
	return false, 0
}

// defaultAlertDedupTTL is how long an identical alert is considered "the same event".
const defaultAlertDedupTTL = 10 * time.Minute

type alertDedupEntry struct {
	at      time.Time
	repeats int
}

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
