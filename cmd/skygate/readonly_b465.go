// readonly_b465.go — the two main.go call sites that make a RESTORED COPY silent.
//
// B465 (2026-11-12). Measured: a skygate brought up from the primary's database, only to
// rehearse a failover, posted REAL Telegram alerts into the operator's chat within the hour
// («пин на exit-узел устарел … состояние узла: offline» about a relay that was online — that
// copy's tailnet never connects to it). The alerts had to stop, and so did every other
// outbound path; the scheduler half of the fix lives in main_schedulers.go, and these are
// the notifier and bot-loop halves.
//
// The code lives HERE rather than in main.go because B372 caps that file's size: the entry
// point only ever shrinks, so a new guard is extracted the moment it is written.
package main

import (
	"database/sql"
	"log"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/handlers"
	"skygate/internal/telegram"
)

// installNotifierUnlessReadOnly installs the real notifier on a normal host and leaves the
// no-op (handlers.New's default) on a read-only copy. Returns whether this process is a
// read-only copy, so the caller can also skip the bot loop.
//
// Why the notifier and not just the message: EVERY alert path (exit-node health, tag drift,
// ACL apply failures, backups, the bot's own replies) ends in this one object. A copy that
// keeps it live writes into another installation's chat, which is what happened.
func installNotifierUnlessReadOnly(app *handlers.App, rn *telegram.RealNotifier) bool {
	if config.ReadOnlyCopy() {
		log.Printf("🛑 READ-ONLY COPY: Telegram notifier NOT installed (alerts from this replica are suppressed); the bot loop is not started either")
		return true
	}
	app.Notifier = rn
	return false
}

// logTelegramStartup announces what the bot will actually do in THIS process.
//
// B465 follow-up, measured on the live restored copy (svyatoslava, 2026-11-12 21:09):
// the old unconditional three-branch message printed
// «🤖 Telegram bot token set (no chat_id yet — receive-only); starting getUpdates loop»
// while the very next statement skipped `rn.Run` because the process was read-only — a
// log line promising the one thing the fix exists to prevent. Nothing polled (no getUpdates
// traffic appeared afterwards), so the CODE was right and the LOG lied: that is the same
// defect class as the stale relay verdict, one level up, and it is what an operator would
// have concluded "the fix is not deployed" from.
//
// So the read-only branch comes FIRST and says what is true; the configured/unconfigured
// split keeps its original wording on a normal host (B391/B422's alert-path contracts read
// the ledger, not this line).
func logTelegramStartup(d *sql.DB, readOnlyCopy bool) {
	if readOnlyCopy {
		log.Printf("🛑 READ-ONLY COPY: Telegram bot loop NOT started (no command is answered by this replica; the stored token is left untouched)")
		return
	}
	if _, _, ok, _ := db.LoadTelegramSendTarget(d); ok {
		log.Printf("🤖 Telegram bot fully configured (token + chat_id); starting getUpdates loop")
	} else if _, _, ok, _ := db.LoadTelegramToken(d); ok {
		log.Printf("🤖 Telegram bot token set (no chat_id yet — receive-only); starting getUpdates loop. Use the 'Send test' button on /admin/telegram to populate chat_id.")
	} else {
		log.Printf("🤖 Telegram bot not configured; hot-swap armed (will re-check DB on every send/poll)")
	}
}
