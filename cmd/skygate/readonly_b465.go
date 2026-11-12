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
	"log"

	"skygate/internal/config"
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
